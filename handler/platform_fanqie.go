// Package handler 提供番茄小说平台的自动发布实现。
package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	c1 "clawstudios/l1_ai_releaser/services/c1_publisher"
)

// FanqiePlatform 番茄小说平台自动发布实现。
type FanqiePlatform struct {
	mgr     *AutoPublishManager
	adapter *c1.FanqiePublishAdapter
}

// NewFanqiePlatform 创建番茄平台发布器。
func NewFanqiePlatform(adapter *c1.FanqiePublishAdapter) *FanqiePlatform {
	return &FanqiePlatform{adapter: adapter}
}

func (p *FanqiePlatform) Platform() string {
	return "fanqie"
}

// SetManager 注入 AutoPublishManager 引用，用于调用公共方法。
func (p *FanqiePlatform) SetManager(mgr *AutoPublishManager) {
	p.mgr = mgr
}

// Run 番茄自动发布主循环（从原 autoPublishLoop 搬迁）。
func (p *FanqiePlatform) Run(job *AutoPublishJob) {
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

		log.Printf("[auto_publish] task=%s ===== 第%d章完成 =====", job.TaskID, staged.chapterNumber)
		job.retryCount = 0
		staged = nil
		time.Sleep(2 * time.Second)
	}
}

// Finalize 番茄手动完结（从原 generateChapter 搬迁）。
func (p *FanqiePlatform) Finalize(job *AutoPublishJob) error {
	if p.adapter == nil {
		return fmt.Errorf("fanqie adapter not configured")
	}

	job.mu.Lock()
	novelName := job.NovelName
	job.mu.Unlock()

	cred, err := p.getCredential(job)
	if err != nil {
		return fmt.Errorf("credential: %w", err)
	}

	taskID := job.TaskID

	log.Printf("[auto_publish] task=%s ===== 开始生成章节 (isFinale=true) =====", taskID)

	platformInfo, pubErr := p.adapter.GetPlatformInfo(job.stopCtx, novelName, cred, job.WorkID)
	if pubErr != nil {
		return fmt.Errorf("get platform info: %s (code=%s)", pubErr.ErrorMessage, pubErr.ErrorCode)
	}

	if platformInfo.WorkID != "" {
		job.mu.Lock()
		job.WorkID = platformInfo.WorkID
		job.mu.Unlock()
	}

	log.Printf("[auto_publish] task=%s 平台状态: workId=%s published=%d drafts=%d",
		taskID, platformInfo.WorkID, len(platformInfo.PublishedChapters), len(platformInfo.Drafts))

	isNewBook := platformInfo.NewlyCreated

	var lastPublished *c1.FanqieLastPublished
	if platformInfo.LastPublished != nil {
		lastPublished = platformInfo.LastPublished
		if platformInfo.LastPublished.ChapterNumber > 0 {
			log.Printf("[auto_publish] task=%s 最新已发布: chapter=%d title=%s",
				taskID, platformInfo.LastPublished.ChapterNumber, platformInfo.LastPublished.Title)
		}
	} else {
		lastPublished = &c1.FanqieLastPublished{ChapterNumber: 0}
	}

	job.mu.Lock()
	currentVolume := job.VolumeName
	currentChapter := job.ChapterNumber
	job.mu.Unlock()

	if currentVolume == "" {
		currentVolume = "第一卷"
	}

	nextChapter, nextVolume := p.determineNextChapter(lastPublished, currentVolume, currentChapter, platformInfo)
	log.Printf("[auto_publish] task=%s 计算下一章: volume=%s chapter=%d (lastPublished=%d currentChapter=%d)",
		taskID, nextVolume, nextChapter, lastPublished.ChapterNumber, currentChapter)

	var volumeId string
	apiVolumeName := nextVolume
	for _, v := range platformInfo.Volumes {
		if strings.Contains(v.VolumeName, nextVolume) {
			volumeId = v.VolumeID
			apiVolumeName = v.VolumeName
			break
		}
	}
	log.Printf("[auto_publish] task=%s 分卷映射: nextVolume=%s apiVolumeName=%s volumeId=%s", taskID, nextVolume, apiVolumeName, volumeId)

	if p.isAlreadyPublished(lastPublished, nextChapter) {
		log.Printf("[auto_publish] task=%s 章节 %d 已在已发布列表中，跳过生成，直接推进号", taskID, nextChapter)
		job.mu.Lock()
		job.ChapterNumber = nextChapter
		job.VolumeName = nextVolume
		job.mu.Unlock()
		p.mgr.updateTaskChapterNumber(job, "", nextChapter)
		return nil
	}

	log.Printf("[auto_publish] task=%s AI 生成章节 chapter=%d vol=%s", taskID, nextChapter, nextVolume)

	job.mu.Lock()
	oldVolume := job.VolumeName
	oldChapter := job.ChapterNumber
	job.VolumeName = nextVolume
	job.ChapterNumber = nextChapter
	job.mu.Unlock()

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
					log.Printf("[auto_publish] task=%s 活跃session=%s 仍在运行中，不关闭，待其自行完成", taskID, existingSID)
				} else {
					log.Printf("[auto_publish] task=%s session=%s 为僵尸，关闭后重试wake", taskID, existingSID)
					p.mgr.closeSessionQuiet(existingSID)
					sessionID, _, err = p.mgr.wakeTask(job, true)
				}
			}
		}
		if err != nil {
			job.mu.Lock()
			job.VolumeName = oldVolume
			job.ChapterNumber = oldChapter
			job.mu.Unlock()
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

	log.Printf("[auto_publish] task=%s AI 生成完成: title=%s contentLen=%d", taskID, chapterTitle, len(draft))

	if chapterTitle == "" {
		chapterTitle = fallbackChapterTitle(draft)
		log.Printf("[auto_publish] task=%s 标题为空，从正文生成兜底标题: %s", taskID, chapterTitle)
	}

	fullTitle := fmt.Sprintf("第%d章 %s", nextChapter, chapterTitle)

	if isNewBook {
		log.Printf("[auto_publish] task=%s 检测到新书, 开始设置书籍信息", taskID)
		p.setNewBookInfo(job, cred, platformInfo, novelName)
	}

	log.Printf("[auto_publish] task=%s 存草稿到平台草稿箱 title=%s chapter=%d", taskID, fullTitle, nextChapter)

	saveResult := p.adapter.SaveDraftViaPageAPI(job.stopCtx, fullTitle, draft, novelName, nextChapter, cred, job.WorkID, apiVolumeName, volumeId)
	if saveResult.Status != "ok" {
		log.Printf("[auto_publish] task=%s API存草稿失败: %s (code=%s), 回退Puppeteer", taskID, saveResult.ErrorMessage, saveResult.ErrorCode)
		saveResult = p.adapter.SaveDraft(job.stopCtx, chapterTitle, draft, novelName, nextChapter, cred, job.WorkID)
		if saveResult.Status != "ok" {
			return &saveDraftRetryError{
				sessionID:    sessionID,
				draft:        draft,
				chapterTitle: chapterTitle,
				chapterNum:   nextChapter,
				volume:       nextVolume,
				err:          fmt.Errorf("save draft: %s (code=%s)", saveResult.ErrorMessage, saveResult.ErrorCode),
			}
		}
		log.Printf("[auto_publish] task=%s Puppeteer兜底存草稿成功: title=%s", taskID, chapterTitle)
	}
	log.Printf("[auto_publish] task=%s 存草稿成功: title=%s", taskID, chapterTitle)

	p.mgr.updateTaskChapterNumber(job, chapterTitle, nextChapter)

	log.Printf("[auto_publish] task=%s 从草稿箱推发布 title=%s fullTitle=%s chapter=%d", taskID, chapterTitle, fullTitle, nextChapter)

	draftItemID := saveResult.DraftItemID
	if draftItemID == "" {
		platformInfo2, pubErr2 := p.adapter.GetPlatformInfo(job.stopCtx, novelName, cred, job.WorkID)
		if pubErr2 != nil {
			log.Printf("[auto_publish] task=%s 获取平台状态失败(发布前): %s", taskID, pubErr2.ErrorMessage)
		} else {
			for _, d := range platformInfo2.Drafts {
				if d.ChapterNumber == nextChapter {
					draftItemID = d.ItemID
					break
				}
			}
		}
	}

	var pubResult *c1.PublishResult
	pubResult = p.adapter.PublishDraftViaPageAPI(job.stopCtx, job.WorkID, draftItemID, fullTitle, draft, apiVolumeName, volumeId, cred)
	if pubResult.Status != "ok" {
		if pubResult.ErrorCode == c1.ErrCodeDailyLimit {
			log.Printf("[auto_publish] task=%s 发布失败(字数超限), 不重试", taskID)
			return fmt.Errorf("publish daily limit: %w", ErrDailyLimitReached)
		}
		log.Printf("[auto_publish] task=%s API发布草稿失败: %s, 回退Puppeteer", taskID, pubResult.ErrorMessage)
		pubResult = p.adapter.PublishDraft(job.stopCtx, chapterTitle, novelName, nextVolume, cred, job.WorkID, draftItemID)
		if pubResult.Status != "ok" {
			if pubResult.ErrorCode == c1.ErrCodeDailyLimit {
				return fmt.Errorf("publish daily limit: %w", ErrDailyLimitReached)
			}
			return &publishRetryError{
				sessionID:    sessionID,
				draftItemID:  draftItemID,
				chapterTitle: chapterTitle,
				volume:       nextVolume,
				err:          fmt.Errorf("publish draft: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode),
			}
		}
		log.Printf("[auto_publish] task=%s Puppeteer兜底发布成功: title=%s postId=%s", taskID, chapterTitle, pubResult.PostID)
	}

	log.Printf("[auto_publish] task=%s 发布草稿成功: title=%s postId=%s", taskID, fullTitle, pubResult.PostID)

	if pubResult.PostID != "" && pubResult.PostID != job.WorkID {
		p.mgr.updatePublishedCount(job)
		p.mgr.saveSessionPostID(job.TaskID, sessionID, pubResult.PostID)
	} else {
		log.Printf("[auto_publish] task=%s postId 无效(workId=%s)，跳过发布计数", taskID, job.WorkID)
	}

	log.Printf("[auto_publish] task=%s ===== 章节生成完成 chapter=%d =====", taskID, nextChapter)
	return nil
}

// phasePrepare 番茄准备阶段：获取凭证 → 查平台状态 → 确定章号 → 设置新书信息。
func (p *FanqiePlatform) phasePrepare(job *AutoPublishJob) *chapterGenState {
	if p.adapter == nil {
		log.Printf("[auto_publish] task=%s fanqie adapter not configured", job.TaskID)
		return nil
	}

	cred, err := p.getCredential(job)
	if err != nil {
		job.retryCount++
		log.Printf("[auto_publish] task=%s 获取凭据失败: %v", job.TaskID, err)
		return nil
	}

	taskID := job.TaskID
	novelName := job.NovelName

	platformInfo, pubErr := p.adapter.GetPlatformInfo(job.stopCtx, novelName, cred, job.WorkID)
	if pubErr != nil {
		job.retryCount++
		log.Printf("[auto_publish] task=%s get_platform_info失败: %s (code=%s)", taskID, pubErr.ErrorMessage, pubErr.ErrorCode)
		return nil
	}

	if platformInfo.WorkID != "" {
		job.mu.Lock()
		job.WorkID = platformInfo.WorkID
		job.mu.Unlock()
	}

	log.Printf("[auto_publish] task=%s 平台状态: workId=%s published=%d drafts=%d",
		taskID, platformInfo.WorkID, len(platformInfo.PublishedChapters), len(platformInfo.Drafts))

	isNewBook := platformInfo.NewlyCreated

	job.mu.Lock()
	currentVolume := job.VolumeName
	currentChapter := job.ChapterNumber
	job.mu.Unlock()

	if currentVolume == "" {
		currentVolume = "第一卷"
	}

	var lastPublished *c1.FanqieLastPublished
	if platformInfo.LastPublished != nil {
		lastPublished = platformInfo.LastPublished
	} else {
		lastPublished = &c1.FanqieLastPublished{ChapterNumber: 0}
	}

	nextChapter, nextVolume := p.determineNextChapter(lastPublished, currentVolume, currentChapter, platformInfo)
	log.Printf("[auto_publish] task=%s 计算下一章: volume=%s chapter=%d (currentChapter=%d)",
		taskID, nextVolume, nextChapter, currentChapter)

	var volumeId string
	apiVolumeName := nextVolume
	for _, v := range platformInfo.Volumes {
		if strings.Contains(v.VolumeName, nextVolume) {
			volumeId = v.VolumeID
			apiVolumeName = v.VolumeName
			break
		}
	}
	log.Printf("[auto_publish] task=%s 分卷映射: nextVolume=%s apiVolumeName=%s volumeId=%s", taskID, nextVolume, apiVolumeName, volumeId)

	if p.isAlreadyPublished(lastPublished, nextChapter) {
		log.Printf("[auto_publish] task=%s 章节%d已发布, 跳过", taskID, nextChapter)
		job.mu.Lock()
		job.ChapterNumber = nextChapter
		job.VolumeName = nextVolume
		job.mu.Unlock()
		p.mgr.updateTaskChapterNumber(job, "", nextChapter)
		return nil
	}

	if isNewBook {
		log.Printf("[auto_publish] task=%s 检测到新书, 开始设置书籍信息", taskID)
		p.setNewBookInfo(job, cred, platformInfo, novelName)
	}

	job.mu.Lock()
	job.VolumeName = nextVolume
	job.ChapterNumber = nextChapter
	job.mu.Unlock()

	return &chapterGenState{
		chapterNumber: nextChapter,
		volume:        nextVolume,
		apiVolumeName: apiVolumeName,
		volumeId:      volumeId,
		platformInfo:  platformInfo,
		cred:          cred,
	}
}

// phaseSaveDraft 番茄存草稿（API优先，Puppeteer兜底）。
func (p *FanqiePlatform) phaseSaveDraft(job *AutoPublishJob, state *chapterGenState) error {
	taskID := job.TaskID
	log.Printf("[auto_publish] task=%s 存草稿 title=%s chapter=%d", taskID, state.fullTitle, state.chapterNumber)

	saveResult := p.adapter.SaveDraftViaPageAPI(job.stopCtx, state.fullTitle, state.draft, job.NovelName, state.chapterNumber, state.cred, job.WorkID, state.apiVolumeName, state.volumeId)
	if saveResult.Status != "ok" {
		log.Printf("[auto_publish] task=%s API存草稿失败: %s (code=%s), 回退Puppeteer", taskID, saveResult.ErrorMessage, saveResult.ErrorCode)
		saveResult = p.adapter.SaveDraft(job.stopCtx, state.chapterTitle, state.draft, job.NovelName, state.chapterNumber, state.cred, job.WorkID)
		if saveResult.Status != "ok" {
			return fmt.Errorf("save draft: %s (code=%s)", saveResult.ErrorMessage, saveResult.ErrorCode)
		}
		log.Printf("[auto_publish] task=%s Puppeteer兜底存草稿成功", taskID)
	}

	state.draftItemID = saveResult.DraftItemID
	log.Printf("[auto_publish] task=%s 存草稿成功: title=%s draftItemID=%s", taskID, state.chapterTitle, state.draftItemID)
	return nil
}

// phasePublishDraft 番茄从草稿箱发布（API优先，Puppeteer兜底）。
func (p *FanqiePlatform) phasePublishDraft(job *AutoPublishJob, state *chapterGenState) error {
	taskID := job.TaskID
	log.Printf("[auto_publish] task=%s 发布章节 title=%s chapter=%d", taskID, state.fullTitle, state.chapterNumber)

	draftItemID := state.draftItemID
	if draftItemID == "" {
		platformInfo2, pubErr2 := p.adapter.GetPlatformInfo(job.stopCtx, job.NovelName, state.cred, job.WorkID)
		if pubErr2 != nil {
			log.Printf("[auto_publish] task=%s 获取平台状态失败(发布前): %s", taskID, pubErr2.ErrorMessage)
		} else {
			for _, d := range platformInfo2.Drafts {
				if d.ChapterNumber == state.chapterNumber {
					draftItemID = d.ItemID
					break
				}
			}
		}
	}

	pubResult := p.adapter.PublishDraftViaPageAPI(job.stopCtx, job.WorkID, draftItemID, state.fullTitle, state.draft, state.apiVolumeName, state.volumeId, state.cred)
	if pubResult.Status != "ok" {
		if pubResult.ErrorCode == c1.ErrCodeDailyLimit {
			return fmt.Errorf("publish daily limit: %w", ErrDailyLimitReached)
		}
		log.Printf("[auto_publish] task=%s API发布失败: %s, 回退Puppeteer", taskID, pubResult.ErrorMessage)
		pubResult = p.adapter.PublishDraft(job.stopCtx, state.chapterTitle, job.NovelName, state.volume, state.cred, job.WorkID, draftItemID)
		if pubResult.Status != "ok" {
			if pubResult.ErrorCode == c1.ErrCodeDailyLimit {
				return fmt.Errorf("publish daily limit: %w", ErrDailyLimitReached)
			}
			return fmt.Errorf("publish draft: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode)
		}
		log.Printf("[auto_publish] task=%s Puppeteer兜底发布成功", taskID)
	}

	log.Printf("[auto_publish] task=%s 发布成功: title=%s postId=%s", taskID, state.fullTitle, pubResult.PostID)

	if pubResult.PostID != "" && pubResult.PostID != job.WorkID {
		p.mgr.updatePublishedCount(job)
		p.mgr.saveSessionPostID(job.TaskID, state.sessionID, pubResult.PostID)
	} else {
		log.Printf("[auto_publish] task=%s postId 无效(workId=%s)，跳过发布计数", taskID, job.WorkID)
	}
	return nil
}

// getCredential 从 A1 密钥库获取 fanqie 平台的 cookie。
func (p *FanqiePlatform) getCredential(job *AutoPublishJob) (string, error) {
	job.mu.Lock()
	accounts := job.Accounts
	job.mu.Unlock()

	if len(accounts) == 0 {
		return "", fmt.Errorf("no accounts configured")
	}

	for _, acc := range accounts {
		if acc["platform"] != "fanqie" {
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
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBody))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.mgr.httpClient.Do(req)
		if err != nil {
			log.Printf("[auto_publish] task=%s 获取凭证失败 account=%s: %v", job.TaskID, accountID, err)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			continue
		}

		var credResp struct {
			Credentials string `json:"credentials"`
		}
		if err := json.Unmarshal(respBody, &credResp); err != nil || credResp.Credentials == "" {
			continue
		}

		return credResp.Credentials, nil
	}

	return "", fmt.Errorf("no fanqie account credential available")
}

// determineNextChapter 根据平台状态计算下一个章号和卷名。
func (p *FanqiePlatform) determineNextChapter(lastPublished *c1.FanqieLastPublished, currentVolume string, currentChapter int, platformInfo *c1.PlatformInfo) (int, string) {
	if lastPublished == nil || lastPublished.ChapterNumber == 0 {
		if currentChapter > 0 {
			return currentChapter + 1, currentVolume
		}
		return 1, currentVolume
	}

	nextChapter := currentChapter + 1
	nextVolume := currentVolume

	if nextChapter <= lastPublished.ChapterNumber {
		nextChapter = lastPublished.ChapterNumber + 1
	}

	if nextVolume == "" {
		nextVolume = "第一卷"
	}
	if nextChapter <= 0 {
		nextChapter = 1
	}

	return nextChapter, nextVolume
}

// isAlreadyPublished 检查章节是否已在已发布列表中。
func (p *FanqiePlatform) isAlreadyPublished(lastPublished *c1.FanqieLastPublished, nextChapter int) bool {
	if lastPublished == nil {
		return false
	}
	return nextChapter <= lastPublished.ChapterNumber
}

// setNewBookInfo 从 skill 元数据中提取书籍信息并调用 SetBookInfo 上传封面和设置分类/简介。
func (p *FanqiePlatform) setNewBookInfo(job *AutoPublishJob, cred string, platformInfo *c1.PlatformInfo, novelName string) {
	name, description, category, roles, fetchErr := p.mgr.fetchSkillMeta(job.SkillID)
	if fetchErr != nil {
		log.Printf("[auto_publish] task=%s 获取skill元信息失败: %v", job.TaskID, fetchErr)
		return
	}
	if platformInfo.BookName != "" {
		name = platformInfo.BookName
	}
	author, authorErr := p.adapter.ResolveAuthorName(job.stopCtx, cred)
	if authorErr != nil {
		log.Printf("[auto_publish] task=%s 获取账号笔名失败: %v, 使用novelName作为fallback", job.TaskID, authorErr)
		author = novelName
	}
	coverBytes, downloadErr := p.mgr.downloadRenderedCover(job.SkillID, author, name)
	if downloadErr != nil {
		log.Printf("[auto_publish] task=%s 下载渲染封面失败: %v", job.TaskID, downloadErr)
		return
	}
	result := p.adapter.SetBookInfo(job.stopCtx, cred, platformInfo.WorkID, name, description, category, roles, coverBytes)
	if result.Status != "ok" {
		log.Printf("[auto_publish] task=%s 设置书籍信息失败: %s (code=%s)", job.TaskID, result.ErrorMessage, result.ErrorCode)
	} else {
		log.Printf("[auto_publish] task=%s 书籍信息设置成功: name=%s", job.TaskID, name)
	}
}

// retryPublishOnly 番茄发布失败重试（仅发布步骤）。
func (p *FanqiePlatform) retryPublishOnly(job *AutoPublishJob, sessionID, draftItemID, chapterTitle, volume string) error {
	cred, err := p.getCredential(job)
	if err != nil {
		return fmt.Errorf("credential: %w", err)
	}

	log.Printf("[auto_publish] task=%s 重试发布: draftItemID=%s chapter=%s volume=%s", job.TaskID, draftItemID, chapterTitle, volume)

	pubResult := p.adapter.PublishDraft(job.stopCtx, chapterTitle, job.NovelName, volume, cred, job.WorkID, draftItemID)
	if pubResult.Status != "ok" {
		return fmt.Errorf("publish draft retry: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode)
	}

	log.Printf("[auto_publish] task=%s 重试发布成功: title=%s postId=%s", job.TaskID, chapterTitle, pubResult.PostID)

	if pubResult.PostID != "" && pubResult.PostID != job.WorkID {
		p.mgr.updatePublishedCount(job)
		p.mgr.saveSessionPostID(job.TaskID, sessionID, pubResult.PostID)
	} else {
		log.Printf("[auto_publish] task=%s 重试发布 postId 无效(workId=%s)，跳过发布计数", job.TaskID, job.WorkID)
	}
	return nil
}

// retrySaveAndPublish 番茄存草稿+发布失败重试。
func (p *FanqiePlatform) retrySaveAndPublish(job *AutoPublishJob, sessionID, draft, chapterTitle string, chapterNum int, volume string) error {
	cred, err := p.getCredential(job)
	if err != nil {
		return fmt.Errorf("credential: %w", err)
	}

	novelName := job.NovelName
	log.Printf("[auto_publish] task=%s 重试存草稿+发布: chapter=%d title=%s volume=%s", job.TaskID, chapterNum, chapterTitle, volume)

	fullTitle := fmt.Sprintf("第%d章 %s", chapterNum, chapterTitle)

	platformInfo, pubErr := p.adapter.GetPlatformInfo(job.stopCtx, novelName, cred, job.WorkID)
	var volumeId, apiVolumeName string
	apiVolumeName = volume
	if pubErr == nil {
		for _, v := range platformInfo.Volumes {
			if strings.Contains(v.VolumeName, volume) {
				volumeId = v.VolumeID
				apiVolumeName = v.VolumeName
				break
			}
		}
	}

	saveResult := p.adapter.SaveDraftViaPageAPI(job.stopCtx, fullTitle, draft, novelName, chapterNum, cred, job.WorkID, apiVolumeName, volumeId)
	if saveResult.Status != "ok" {
		log.Printf("[auto_publish] task=%s 重试API存草稿失败: %s, 回退Puppeteer", job.TaskID, saveResult.ErrorMessage)
		saveResult = p.adapter.SaveDraft(job.stopCtx, chapterTitle, draft, novelName, chapterNum, cred, job.WorkID)
		if saveResult.Status != "ok" {
			return fmt.Errorf("save draft retry: %s (code=%s)", saveResult.ErrorMessage, saveResult.ErrorCode)
		}
	}
	log.Printf("[auto_publish] task=%s 重试存草稿成功: title=%s", job.TaskID, chapterTitle)

	p.mgr.updateTaskChapterNumber(job, chapterTitle, chapterNum)

	draftItemID := saveResult.DraftItemID
	if draftItemID == "" {
		platformInfo2, pubErr2 := p.adapter.GetPlatformInfo(job.stopCtx, novelName, cred, job.WorkID)
		if pubErr2 == nil {
			for _, d := range platformInfo2.Drafts {
				if d.ChapterNumber == chapterNum {
					draftItemID = d.ItemID
					break
				}
			}
		}
	}

	pubResult := p.adapter.PublishDraft(job.stopCtx, chapterTitle, novelName, volume, cred, job.WorkID, draftItemID)
	if pubResult.Status != "ok" {
		return fmt.Errorf("publish draft retry: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode)
	}

	log.Printf("[auto_publish] task=%s 重试发布成功: title=%s postId=%s", job.TaskID, chapterTitle, pubResult.PostID)

	if pubResult.PostID != "" && pubResult.PostID != job.WorkID {
		p.mgr.updatePublishedCount(job)
		p.mgr.saveSessionPostID(job.TaskID, sessionID, pubResult.PostID)
	}
	return nil
}
