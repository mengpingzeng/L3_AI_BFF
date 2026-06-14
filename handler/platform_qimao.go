// Package handler 提供七猫小说平台的自动发布实现。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	c1 "clawstudios/l1_ai_releaser/services/c1_publisher"
)

// QimaoPlatform 七猫小说平台自动发布实现。
type QimaoPlatform struct {
	mgr     *AutoPublishManager
	adapter *c1.QimaoPublishAdapter
}

// NewQimaoPlatform 创建七猫平台发布器。
func NewQimaoPlatform(adapter *c1.QimaoPublishAdapter) *QimaoPlatform {
	return &QimaoPlatform{adapter: adapter}
}

func (p *QimaoPlatform) Platform() string {
	return "qimao"
}

// SetManager 注入 AutoPublishManager 引用。
func (p *QimaoPlatform) SetManager(mgr *AutoPublishManager) {
	p.mgr = mgr
}

// Run 七猫自动发布主循环（4 阶段流水线）。
func (p *QimaoPlatform) Run(job *AutoPublishJob) {
	const maxRetries = 3
	job.retryCount = 0

	var staged *chapterGenState

	for {
		if staged == nil {
			select {
			case <-job.stopCtx.Done():
				p.mgr.cleanupSessions(job)
				p.mgr.updateJobStatus(job.TaskID, "stopped")
				if job.onExit != nil {
					job.onExit(job, "stopped")
				}
				return
			default:
			}

			staged = p.phasePrepare(job)
			if staged == nil {
				continue
			}
		}

		select {
		case <-job.stopCtx.Done():
			p.mgr.cleanupSessions(job)
			p.mgr.updateJobStatus(job.TaskID, "stopped")
			if job.onExit != nil {
				job.onExit(job, "stopped")
			}
			return
		default:
		}

		if staged.draft == "" {
			if err := p.mgr.phaseGenerate(job, staged); err != nil {
				if errors.Is(err, ErrDailyLimitReached) {
					log.Printf("[auto_publish] task=%s DAILY_LIMIT(AI生成), 退出并重新入队", job.TaskID)
					p.mgr.cleanupSessions(job)
					if job.onExitRequeue != nil {
						job.onExitRequeue(job, err)
					}
					return
				}
				job.retryCount++
				if job.retryCount > maxRetries {
					log.Printf("[auto_publish] task=%s AI生成连续失败%d次, 退出并重新入队", job.TaskID, maxRetries)
					p.mgr.cleanupSessions(job)
					if job.onExitRequeue != nil {
						job.onExitRequeue(job, err)
					}
					return
				}
				log.Printf("[auto_publish] task=%s AI生成失败(第%d/%d次): %v, 1分钟后重试",
					job.TaskID, job.retryCount, maxRetries, err)
				p.mgr.sleepOrStop(job, 1*time.Minute)
				continue
			}
		}

		if staged.draftItemID == "" {
			if err := p.phaseSaveDraft(job, staged); err != nil {
				if errors.Is(err, ErrDailyLimitReached) {
					log.Printf("[auto_publish] task=%s DAILY_LIMIT(存草稿), 退出并重新入队", job.TaskID)
					p.mgr.cleanupSessions(job)
					if job.onExitRequeue != nil {
						job.onExitRequeue(job, err)
					}
					return
				}
				job.retryCount++
				if job.retryCount > maxRetries {
					log.Printf("[auto_publish] task=%s 存草稿连续失败%d次, 退出并重新入队", job.TaskID, maxRetries)
					p.mgr.cleanupSessions(job)
					if job.onExitRequeue != nil {
						job.onExitRequeue(job, err)
					}
					return
				}
				log.Printf("[auto_publish] task=%s 存草稿失败(第%d/%d次): %v, 1分钟后重试",
					job.TaskID, job.retryCount, maxRetries, err)
				p.mgr.sleepOrStop(job, 1*time.Minute)
				continue
			}
			p.mgr.updateTaskChapterNumber(job, staged.chapterTitle, staged.chapterNumber)
		}

		if err := p.phasePublishDraft(job, staged); err != nil {
			if errors.Is(err, ErrDailyLimitReached) {
				log.Printf("[auto_publish] task=%s DAILY_LIMIT(发布), 退出并重新入队", job.TaskID)
				p.mgr.cleanupSessions(job)
				if job.onExitRequeue != nil {
					job.onExitRequeue(job, err)
				}
				return
			}
			job.retryCount++
			if job.retryCount > maxRetries {
				log.Printf("[auto_publish] task=%s 发布连续失败%d次, 退出并重新入队", job.TaskID, maxRetries)
				p.mgr.cleanupSessions(job)
				if job.onExitRequeue != nil {
					job.onExitRequeue(job, err)
				}
				return
			}
			log.Printf("[auto_publish] task=%s 发布失败(第%d/%d次): %v, 1分钟后重试",
				job.TaskID, job.retryCount, maxRetries, err)
			p.mgr.sleepOrStop(job, 1*time.Minute)
			continue
		}

		log.Printf("[auto_publish] task=%s ===== 第%d章完成(qimao) =====", job.TaskID, staged.chapterNumber)
		job.retryCount = 0
		staged = nil
		time.Sleep(2 * time.Second)
	}
}

// Finalize 七猫手动完结。
func (p *QimaoPlatform) Finalize(job *AutoPublishJob) error {
	if p.adapter == nil {
		return fmt.Errorf("qimao adapter not configured")
	}

	job.mu.Lock()
	novelName := job.NovelName
	job.mu.Unlock()

	cred, err := p.getCredential(job)
	if err != nil {
		return fmt.Errorf("credential: %w", err)
	}

	taskID := job.TaskID
	log.Printf("[auto_publish] task=%s ===== 开始生成章节(qimao, isFinale=true) =====", taskID)

	platformInfo, pubErr := p.adapter.GetPlatformInfo(job.stopCtx, novelName, cred)
	if pubErr != nil {
		return fmt.Errorf("get platform info: %s (code=%s)", pubErr.ErrorMessage, pubErr.ErrorCode)
	}

	bookID := platformInfo.BookID
	if !platformInfo.BookExists || bookID == "" {
		bookID = p.ensureBookExists(job, cred, novelName)
		if bookID == "" {
			return fmt.Errorf("failed to get book_id for novel: %s", novelName)
		}
	}

	chapters, chErr := p.adapter.GetChapterList(job.stopCtx, bookID, cred)
	if chErr != nil {
		return fmt.Errorf("get chapter list: %s (code=%s)", chErr.ErrorMessage, chErr.ErrorCode)
	}

	nextChapter := chapters.MaxNameIndex + 1
	if nextChapter <= 0 {
		nextChapter = 1
	}

	job.mu.Lock()
	currentVolume := job.VolumeName
	job.ChapterNumber = nextChapter
	job.WorkID = bookID
	job.mu.Unlock()

	if currentVolume == "" {
		currentVolume = "第一卷"
	}

	log.Printf("[auto_publish] task=%s 计算下一章(qimao): chapter=%d bookId=%s", taskID, nextChapter, bookID)

	sessionID, _, err := p.mgr.wakeTask(job, true)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "already has active session") || strings.Contains(errStr, "active session") {
			existingSID := p.mgr.extractSessionFromError(errStr)
			if existingSID == "" {
				sessions, fetchErr := p.mgr.fetchSessions(taskID)
				if fetchErr == nil && len(sessions) > 0 {
					existingSID = sessions[0].SessionID
				}
			}
			if existingSID != "" {
				if p.mgr.isSessionAlive(existingSID) {
					log.Printf("[auto_publish] task=%s 活跃session=%s 仍在运行中", taskID, existingSID)
				} else {
					log.Printf("[auto_publish] task=%s session=%s 为僵尸，关闭后重试wake", taskID, existingSID)
					p.mgr.closeSessionQuiet(existingSID)
					sessionID, _, err = p.mgr.wakeTask(job, true)
				}
			}
		}
		if err != nil {
			return fmt.Errorf("wake task: %w", err)
		}
	}
	log.Printf("[auto_publish] task=%s session=%s 已创建", taskID, sessionID)

	draft, chapterTitle, draftVersion, err := p.mgr.waitForSession(job, sessionID)
	if err != nil {
		p.mgr.closeSessionQuiet(sessionID)
		return fmt.Errorf("wait for session: %w", err)
	}
	p.mgr.closeSessionQuiet(sessionID)

	job.mu.Lock()
	job.DraftVersion = draftVersion
	job.mu.Unlock()

	log.Printf("[auto_publish] task=%s AI 生成完成(qimao): title=%s contentLen=%d", taskID, chapterTitle, len(draft))
	if chapterTitle == "" {
		chapterTitle = fallbackChapterTitle(draft)
	}

	fullTitle := fmt.Sprintf("第%d章 %s", nextChapter, chapterTitle)
	log.Printf("[auto_publish] task=%s 存草稿(qimao) title=%s chapter=%d bookId=%s", taskID, fullTitle, nextChapter, bookID)

	saveResult := p.adapter.SaveDraft(job.stopCtx, bookID, fullTitle, draft, nextChapter, cred)
	if saveResult.Status != "ok" {
		return fmt.Errorf("save draft: %s (code=%s)", saveResult.ErrorMessage, saveResult.ErrorCode)
	}

	time.Sleep(3 * time.Second)

	chapters2, chErr2 := p.adapter.GetChapterList(job.stopCtx, bookID, cred)
	if chErr2 != nil {
		return fmt.Errorf("get chapter list after save: %s (code=%s)", chErr2.ErrorMessage, chErr2.ErrorCode)
	}

	targetChapterID := p.adapter.MatchDraftChapterID(chapters2, nextChapter)
	if targetChapterID == "" {
		return fmt.Errorf("cannot match draft chapter_id for chapter %d", nextChapter)
	}
	log.Printf("[auto_publish] task=%s 匹配草稿 chapterId=%s", taskID, targetChapterID)

	pubResult := p.adapter.PublishDraft(job.stopCtx, bookID, targetChapterID, cred)
	if pubResult.Status != "ok" {
		if pubResult.ErrorCode == c1.ErrCodeDailyLimit {
			return fmt.Errorf("publish daily limit: %w", ErrDailyLimitReached)
		}
		return fmt.Errorf("publish draft: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode)
	}

	log.Printf("[auto_publish] task=%s 发布成功(qimao): chapter=%d postId=%s", taskID, nextChapter, pubResult.PostID)
	p.mgr.updatePublishedCount(job)
	p.mgr.saveSessionPostID(job.TaskID, sessionID, pubResult.PostID)
	p.mgr.updateTaskChapterNumber(job, chapterTitle, nextChapter)

	return nil
}

// phasePrepare 七猫准备阶段。
func (p *QimaoPlatform) phasePrepare(job *AutoPublishJob) *chapterGenState {
	if p.adapter == nil {
		log.Printf("[auto_publish] task=%s qimao adapter not configured", job.TaskID)
		return nil
	}

	cred, err := p.getCredential(job)
	if err != nil {
		job.retryCount++
		log.Printf("[auto_publish] task=%s 获取凭据失败(qimao): %v", job.TaskID, err)
		return nil
	}

	taskID := job.TaskID
	novelName := job.NovelName

	platformInfo, pubErr := p.adapter.GetPlatformInfo(job.stopCtx, novelName, cred)
	if pubErr != nil {
		job.retryCount++
		log.Printf("[auto_publish] task=%s get_platform_info失败(qimao): %s (code=%s)", taskID, pubErr.ErrorMessage, pubErr.ErrorCode)
		return nil
	}

	bookID := platformInfo.BookID
	if !platformInfo.BookExists || bookID == "" {
		bookID = p.ensureBookExists(job, cred, novelName)
		if bookID == "" {
			job.retryCount++
			log.Printf("[auto_publish] task=%s 获取book_id失败(qimao)", taskID)
			return nil
		}
	}

	job.mu.Lock()
	job.WorkID = bookID
	job.mu.Unlock()

	log.Printf("[auto_publish] task=%s 平台状态(qimao): bookId=%s bookExists=%v", taskID, bookID, platformInfo.BookExists)

	chapters, chErr := p.adapter.GetChapterList(job.stopCtx, bookID, cred)
	if chErr != nil {
		job.retryCount++
		log.Printf("[auto_publish] task=%s get_chapter_list失败(qimao): %s (code=%s)", taskID, chErr.ErrorMessage, chErr.ErrorCode)
		return nil
	}

	maxPublished := chapters.MaxNameIndex
	log.Printf("[auto_publish] task=%s 章节状态(qimao): maxPublished=%d totalChapters=%d",
		taskID, maxPublished, len(chapters.Chapters))

	job.mu.Lock()
	currentVolume := job.VolumeName
	currentChapter := job.ChapterNumber
	job.mu.Unlock()

	if currentVolume == "" {
		currentVolume = "第一卷"
	}

	nextChapter := maxPublished + 1
	if currentChapter > nextChapter {
		nextChapter = currentChapter
	}
	if nextChapter <= 0 {
		nextChapter = 1
	}

	log.Printf("[auto_publish] task=%s 计算下一章(qimao): chapter=%d (maxPublished=%d currentChapter=%d)",
		taskID, nextChapter, maxPublished, currentChapter)

	job.mu.Lock()
	job.ChapterNumber = nextChapter
	job.VolumeName = currentVolume
	job.mu.Unlock()

	return &chapterGenState{
		chapterNumber: nextChapter,
		volume:        currentVolume,
		cred:          cred,
	}
}

// phaseSaveDraft 七猫存草稿。
func (p *QimaoPlatform) phaseSaveDraft(job *AutoPublishJob, state *chapterGenState) error {
	taskID := job.TaskID
	bookID := job.WorkID

	log.Printf("[auto_publish] task=%s 存草稿(qimao) title=%s chapter=%d bookId=%s",
		taskID, state.fullTitle, state.chapterNumber, bookID)

	saveResult := p.adapter.SaveDraft(job.stopCtx, bookID, state.fullTitle, state.draft, state.chapterNumber, state.cred)
	if saveResult.Status != "ok" {
		return fmt.Errorf("save draft: %s (code=%s)", saveResult.ErrorMessage, saveResult.ErrorCode)
	}

	log.Printf("[auto_publish] task=%s 存草稿成功(qimao), 等待平台写入...", taskID)
	time.Sleep(3 * time.Second)

	chapters, chErr := p.adapter.GetChapterList(job.stopCtx, bookID, state.cred)
	if chErr == nil {
		draftChapterID := p.adapter.MatchDraftChapterID(chapters, state.chapterNumber)
		if draftChapterID != "" {
			state.draftItemID = draftChapterID
			log.Printf("[auto_publish] task=%s 匹配草稿 chapterId=%s", taskID, draftChapterID)
		} else {
			log.Printf("[auto_publish] task=%s 警告: 未匹配到草稿 chapterId, 将在发布阶段重试", taskID)
		}
	} else {
		log.Printf("[auto_publish] task=%s 获取章节列表失败(qimao): %s", taskID, chErr.ErrorMessage)
	}

	return nil
}

// phasePublishDraft 七猫从草稿箱发布。
func (p *QimaoPlatform) phasePublishDraft(job *AutoPublishJob, state *chapterGenState) error {
	taskID := job.TaskID
	bookID := job.WorkID

	log.Printf("[auto_publish] task=%s 发布章节(qimao) title=%s chapter=%d bookId=%s draftItemId=%s",
		taskID, state.fullTitle, state.chapterNumber, bookID, state.draftItemID)

	draftChapterID := state.draftItemID
	if draftChapterID == "" {
		chapters, chErr := p.adapter.GetChapterList(job.stopCtx, bookID, state.cred)
		if chErr != nil {
			return fmt.Errorf("get chapter list before publish: %s (code=%s)", chErr.ErrorMessage, chErr.ErrorCode)
		}
		draftChapterID = p.adapter.MatchDraftChapterID(chapters, state.chapterNumber)
		if draftChapterID == "" {
			return fmt.Errorf("cannot find draft chapter_id for chapter %d", state.chapterNumber)
		}
		log.Printf("[auto_publish] task=%s 重新匹配草稿 chapterId=%s", taskID, draftChapterID)
	}

	pubResult := p.adapter.PublishDraft(job.stopCtx, bookID, draftChapterID, state.cred)
	if pubResult.Status != "ok" {
		if pubResult.ErrorCode == c1.ErrCodeDailyLimit {
			return fmt.Errorf("publish daily limit: %w", ErrDailyLimitReached)
		}
		return fmt.Errorf("publish draft: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode)
	}

	log.Printf("[auto_publish] task=%s 发布成功(qimao): title=%s postId=%s", taskID, state.fullTitle, pubResult.PostID)

	if pubResult.PostID != "" {
		p.mgr.updatePublishedCount(job)
		p.mgr.saveSessionPostID(job.TaskID, state.sessionID, pubResult.PostID)
	} else {
		log.Printf("[auto_publish] task=%s postId 为空, 跳过发布计数", taskID)
	}

	return nil
}

// ensureBookExists 确保七猫平台上存在对应作品，不存在则创建。返回 book_id。
func (p *QimaoPlatform) ensureBookExists(job *AutoPublishJob, cred, novelName string) string {
	log.Printf("[auto_publish] task=%s 作品不存在(qimao), 开始创建...", job.TaskID)

	_, description, _, roles, fetchErr := p.mgr.fetchSkillMeta(job.SkillID)
	p.logRolesTrace(job.TaskID, "ensureBookExists.fetchSkillMeta", roles, fetchErr)
	if fetchErr != nil {
		log.Printf("[auto_publish] task=%s 获取skill元信息失败(qimao): %v", job.TaskID, fetchErr)
	}
	if description == "" {
		description = "精彩小说"
	}

	bookOpt, optErr := p.adapter.GetBookOption(job.stopCtx, cred)
	if optErr != nil {
		log.Printf("[auto_publish] task=%s 获取建书选项失败(qimao): %s", job.TaskID, optErr.ErrorMessage)
	}

	p.logRolesTrace(job.TaskID, "ensureBookExists.beforeCreateBook", fmt.Sprintf("novelName=%s description=[%d]chars roles=%q", novelName, len(description), roles), nil)
	createResult := p.adapter.CreateBook(job.stopCtx, cred, novelName, description, roles, nil, bookOpt)
	if createResult.Status == "ok" && createResult.PostID != "" {
		log.Printf("[auto_publish] task=%s 创建作品成功(qimao): bookId=%s", job.TaskID, createResult.PostID)
		p.setNewBookInfo(job, cred, createResult.PostID, novelName)
		return createResult.PostID
	}

	altNames := []string{novelName + "之续", novelName + "新篇"}
	for _, altName := range altNames {
		log.Printf("[auto_publish] task=%s 重试创建作品(qimao): altName=%s", job.TaskID, altName)
		createResult = p.adapter.CreateBook(job.stopCtx, cred, altName, description, roles, nil, bookOpt)
		if createResult.Status == "ok" && createResult.PostID != "" {
			log.Printf("[auto_publish] task=%s 创建作品成功(qimao,alt): bookId=%s altName=%s",
				job.TaskID, createResult.PostID, altName)
		p.setNewBookInfo(job, cred, createResult.PostID, altName)
			return createResult.PostID
		}
	}

	log.Printf("[auto_publish] task=%s 创建作品全部重试失败(qimao)", job.TaskID)
	return ""
}

// setNewBookInfo 从 skill 元数据中提取书籍信息并调用 SetBookInfo 上传封面和设置分类/简介。
func (p *QimaoPlatform) setNewBookInfo(job *AutoPublishJob, cred, bookId, novelName string) {
	_, description, category, roles, fetchErr := p.mgr.fetchSkillMeta(job.SkillID)
	if fetchErr != nil {
		log.Printf("[auto_publish] task=%s 获取skill元信息失败(qimao): %v", job.TaskID, fetchErr)
		return
	}

	author, authorErr := p.adapter.ResolveAuthorName(job.stopCtx, cred)
	if authorErr != nil {
		log.Printf("[auto_publish] task=%s 获取账号笔名失败(qimao): %v, 使用novelName作为fallback", job.TaskID, authorErr)
		author = novelName
	}

	coverBytes, downloadErr := p.mgr.downloadRenderedCover(job.SkillID, author, novelName)
	if downloadErr != nil {
		log.Printf("[auto_publish] task=%s 下载渲染封面失败(qimao): %v", job.TaskID, downloadErr)
		return
	}
	_ = os.WriteFile(fmt.Sprintf("/tmp/logs/qimao_cover_raw_%s.png", job.TaskID), coverBytes, 0644)

	bookOpt, optErr := p.adapter.GetBookOption(job.stopCtx, cred)
	if optErr != nil {
		log.Printf("[auto_publish] task=%s 获取建书选项失败(qimao): %s", job.TaskID, optErr.ErrorMessage)
	}
	category2 := ""
	tagIds := ""
	if bookOpt != nil {
		if bookOpt.Category2 != "" {
			category2 = bookOpt.Category2
		}
		if bookOpt.PickedTagIds != "" {
			tagIds = bookOpt.PickedTagIds
		} else {
			tagIds = p.adapter.PickTags(bookOpt)
		}
	}

	result := p.adapter.SetBookInfo(job.stopCtx, cred, bookId, novelName, description, category, category2, tagIds, roles, coverBytes)
	if result.Status != "ok" {
		log.Printf("[auto_publish] task=%s 设置书籍信息失败(qimao): %s (code=%s)", job.TaskID, result.ErrorMessage, result.ErrorCode)
	} else {
		log.Printf("[auto_publish] task=%s 书籍信息设置成功(qimao): bookId=%s name=%s", job.TaskID, bookId, novelName)
	}
}

// getCredential 从 A1 密钥库获取 qimao 平台的 cookie。
func (p *QimaoPlatform) getCredential(job *AutoPublishJob) (string, error) {
	job.mu.Lock()
	accounts := job.Accounts
	job.mu.Unlock()

	if len(accounts) == 0 {
		return "", fmt.Errorf("no accounts configured")
	}

	for _, acc := range accounts {
		if acc["platform"] != "qimao" {
			continue
		}
		accountID := acc["accountId"]
		uid := acc["uid"]

		url := fmt.Sprintf("%s/api/account/credentials", p.mgr.a1BaseURL)
		body := map[string]interface{}{
			"account_id": accountID,
			"uid":        uid,
			"caller":     "c1_publisher",
		}
		jsonBody, err := json.Marshal(body)
		if err != nil {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(jsonBody)))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.mgr.httpClient.Do(req)
		if err != nil {
			log.Printf("[auto_publish] task=%s 获取凭证失败(qimao) account=%s: %v", job.TaskID, accountID, err)
			continue
		}

		respBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			continue
		}

		var credResp struct {
			Credentials string `json:"credentials"`
		}
		if err := json.Unmarshal(respBytes, &credResp); err != nil || credResp.Credentials == "" {
			continue
		}

		return credResp.Credentials, nil
	}
	return "", fmt.Errorf("no qimao account credential available")
}

func (p *QimaoPlatform) logRolesTrace(taskID, step string, roles string, fetchErr error) {
	f, err := os.OpenFile("/tmp/logs/roles_trace.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	errStr := "nil"
	if fetchErr != nil {
		errStr = fetchErr.Error()
	}
	fmt.Fprintf(f, "[%s] task=%s step=%s roles(len=%d)=%q err=%s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), taskID, step, len(roles), roles, errStr)
}
