package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	c1 "clawstudios/l1_ai_releaser/services/c1_publisher"
	"clawstudios/pkg/logging"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/pkg/validator"
	"github.com/gin-gonic/gin"
)

const (
	sessionPollInterval = 3 * time.Second
	sessionWaitTimeout  = 15 * time.Minute
)

type AutoPublishManager struct {
	jobs              map[string]*AutoPublishJob
	mu                sync.RWMutex
	sessionMgrURL     string
	workflowURL       string
	accountURL        string
	skillRegistryURL  string
	httpClient        *http.Client
	stoppedTasksFile  string
	stoppedTasks      map[string]bool
	stoppedMu         sync.RWMutex
	fanqieAdapter     *c1.FanqiePublishAdapter
	a1BaseURL         string
}

type AutoPublishJob struct {
	TaskID        string
	UserID        string
	Platform      string
	Accounts      []map[string]string
	SkillID       string
	Topic         string
	NovelName     string
	VolumeName    string
	ChapterNumber int
	DraftVersion  int
	Status        string
	WorkID        string
	stopCtx       context.Context
	stopCancel    context.CancelFunc
	finishCh      chan struct{}
	mu            sync.Mutex
	createdAt     time.Time
}

func NewAutoPublishManager(sessionMgrURL, workflowURL, accountURL, skillRegistryURL, stoppedTasksFile string, fanqieAdapter *c1.FanqiePublishAdapter, a1BaseURL string) *AutoPublishManager {
	m := &AutoPublishManager{
		jobs:             make(map[string]*AutoPublishJob),
		sessionMgrURL:    sessionMgrURL,
		workflowURL:      workflowURL,
		accountURL:       accountURL,
		skillRegistryURL: skillRegistryURL,
		stoppedTasksFile: stoppedTasksFile,
		stoppedTasks:     make(map[string]bool),
		fanqieAdapter:    fanqieAdapter,
		a1BaseURL:        a1BaseURL,
		httpClient: &http.Client{
			Timeout: 600 * time.Second,
		},
	}
	if stoppedTasksFile != "" {
		m.loadStoppedTasks()
	}
	return m
}

// ========== 兼容层方法 ==========

func (m *AutoPublishManager) RecordStoppedTask(taskID string) {
	m.stoppedMu.Lock()
	m.stoppedTasks[taskID] = true
	m.stoppedMu.Unlock()
	m.saveStoppedTasks()
}

func (m *AutoPublishManager) IsStopped(taskID string) bool {
	m.stoppedMu.RLock()
	defer m.stoppedMu.RUnlock()
	return m.stoppedTasks[taskID]
}

func (m *AutoPublishManager) IsAutoPublishActive(taskID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[taskID]
	return ok && job != nil && job.Status == "running"
}

func (m *AutoPublishManager) ReloadStoppedTasks() {
	m.loadStoppedTasks()
}

func (m *AutoPublishManager) loadStoppedTasks() {
	if m.stoppedTasksFile == "" {
		return
	}
	data, err := os.ReadFile(m.stoppedTasksFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[auto_publish] 加载停止任务文件失败: %v", err)
		}
		return
	}
	m.stoppedMu.Lock()
	defer m.stoppedMu.Unlock()
	var tasks map[string]bool
	if err := json.Unmarshal(data, &tasks); err != nil {
		log.Printf("[auto_publish] 解析停止任务文件失败: %v", err)
		return
	}
	m.stoppedTasks = tasks
}

func (m *AutoPublishManager) saveStoppedTasks() {
	if m.stoppedTasksFile == "" {
		return
	}
	m.stoppedMu.RLock()
	data, err := json.Marshal(m.stoppedTasks)
	m.stoppedMu.RUnlock()
	if err != nil {
		return
	}
	os.WriteFile(m.stoppedTasksFile, data, 0644)
}

func (m *AutoPublishManager) StartAutoPublishInternal(uid, role string, req model.AutoPublishStartReq) error {
	m.mu.RLock()
	existing, exists := m.jobs[req.TaskID]
	m.mu.RUnlock()
	if exists {
		existing.mu.Lock()
		status := existing.Status
		existing.mu.Unlock()
		if status == "running" || status == "finishing" {
			return fmt.Errorf("任务 %s 已有自动发布在运行中", req.TaskID)
		}
	}

	taskInfo, err := m.fetchTaskInfo(req.TaskID)
	if err != nil {
		return fmt.Errorf("任务 %s 不存在", req.TaskID)
	}

	platform := req.Platform
	if platform == "" {
		platform = taskInfo.Platform
	}

	accounts, err := m.resolveAccounts(uid, role, platform, req.Accounts)
	if err != nil {
		return err
	}

	skillID := req.SkillID
	if skillID == "" {
		skillID = taskInfo.SkillID
	}
	if skillID == "" {
		skillID = "general_fallback_v1"
	}

	topic := req.Topic
	if topic == "" {
		topic = taskInfo.Topic
	}

	novelName := req.NovelName
	if novelName == "" {
		novelName = taskInfo.NovelName
	}

	volumeName := req.VolumeName
	if volumeName == "" {
		volumeName = taskInfo.VolumeName
	}

	chapterNumber := taskInfo.ChapterNumber
	if chapterNumber <= 0 {
		chapterNumber = taskInfo.SessionCount
	}

	stopCtx, stopCancel := context.WithCancel(context.Background())
	job := &AutoPublishJob{
		TaskID:        req.TaskID,
		UserID:        uid,
		Platform:      platform,
		Accounts:      accounts,
		SkillID:       skillID,
		Topic:         topic,
		NovelName:     novelName,
		VolumeName:    volumeName,
		ChapterNumber: chapterNumber,
		DraftVersion:  taskInfo.SessionCount,
		Status:        "running",
		stopCtx:       stopCtx,
		stopCancel:    stopCancel,
		finishCh:      make(chan struct{}, 1),
		createdAt:     time.Now(),
	}

	m.mu.Lock()
	m.jobs[req.TaskID] = job
	m.mu.Unlock()

	log.Printf("[auto_publish] 自动发布已启动: task=%s platform=%s skill=%s", req.TaskID, platform, skillID)

	go m.autoPublishLoop(job)
	return nil
}

func (m *AutoPublishManager) resolveAccounts(uid, role, platform string, accountIDs []string) ([]map[string]string, error) {
	var accounts []map[string]string
	if len(accountIDs) > 0 {
		uidForLookup := uid
		if role == "admin" {
			uidForLookup = ""
		}
		allUserAccounts := fetchUserAccounts(m.accountURL, uidForLookup, platform)
		userAccountSet := make(map[string]string)
		for _, a := range allUserAccounts {
			userAccountSet[a.AccountID] = a.Platform
		}
		for _, accID := range accountIDs {
			accPlatform, ok := userAccountSet[accID]
			if role != "admin" && !ok {
				return nil, fmt.Errorf("账号 %s 不属于当前用户或未绑定", accID)
			}
			if !ok {
				accPlatform = platform
			}
			accounts = append(accounts, map[string]string{
				"accountId": accID,
				"uid":       uid,
				"platform":  accPlatform,
			})
		}
	} else {
		uidForLookup := uid
		if role == "admin" {
			uidForLookup = ""
		}
		realAccounts := fetchUserAccounts(m.accountURL, uidForLookup, platform)
		if len(realAccounts) == 0 {
			return nil, fmt.Errorf("没有绑定 %s 平台的账号", platform)
		}
		for _, a := range realAccounts {
			accounts = append(accounts, map[string]string{
				"accountId": a.AccountID,
				"uid":       uid,
				"platform":  a.Platform,
			})
		}
	}
	return accounts, nil
}

// ========== HTTP Handlers ==========

func (m *AutoPublishManager) StartAutoPublish() gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)

		var req model.AutoPublishStartReq
		if err := c.ShouldBindJSON(&req); err != nil {
			if logger != nil {
				logger.Error(logging.ErrInvalidParam, "自动发布: JSON解析失败: %v", err)
			}
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		if !validator.IsValidTaskID(req.TaskID) {
			model.Error(c, model.ErrInvalidParam.WithDetail("任务 ID 格式不合法"))
			return
		}

		uidVal, _ := c.Get("uid")
		roleVal, _ := c.Get("role")
		uid := uidVal.(string)
		role := roleVal.(string)

		if err := m.StartAutoPublishInternal(uid, role, req); err != nil {
			if logger != nil {
				logger.Error(logging.ErrInternal, "自动发布启动失败: %v", err)
			}
			msg := err.Error()
			if strings.Contains(msg, "已有自动发布") {
				model.Error(c, model.ErrConflict.WithDetail(msg))
			} else if strings.Contains(msg, "不存在") {
				model.Error(c, model.ErrNotFound.WithDetail(msg))
			} else {
				model.Error(c, model.ErrInvalidParam.WithDetail(msg))
			}
			return
		}

		tid, _ := c.Get(model.TraceIDKey)
		c.JSON(200, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data: map[string]interface{}{
				"task_id": req.TaskID,
				"status":  "running",
			},
			TraceID: tid.(string),
		})
	}
}

func (m *AutoPublishManager) StopAutoPublish() gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)

		var req model.AutoPublishStopReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		if req.TaskID == "" || req.UserID == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("task_id 和 user_id 不能为空"))
			return
		}

		m.mu.RLock()
		job, exists := m.jobs[req.TaskID]
		m.mu.RUnlock()

		if !exists {
			m.RecordStoppedTask(req.TaskID)
			model.Error(c, model.ErrNotFound.WithDetail(fmt.Sprintf("任务 %s 没有正在执行的自动发布", req.TaskID)))
			return
		}

		if job.UserID != req.UserID {
			model.Error(c, model.ErrUnauthorized.WithDetail("无权停止此任务的自动发布"))
			return
		}

		job.mu.Lock()
		if job.Status == "stopped" || job.Status == "completed" {
			job.mu.Unlock()
			m.RecordStoppedTask(req.TaskID)
			tid, _ := c.Get(model.TraceIDKey)
			c.JSON(200, model.APIResponse{
				Code:    0,
				Message: "ok",
				Data: map[string]interface{}{
					"task_id": req.TaskID,
					"status":  job.Status,
				},
				TraceID: tid.(string),
			})
			return
		}
		job.Status = "stopping"
		job.mu.Unlock()

		m.RecordStoppedTask(req.TaskID)

		job.stopCancel()

		if logger != nil {
			logger.Info("自动发布已停止: task=%s", req.TaskID)
		}

		tid, _ := c.Get(model.TraceIDKey)
		c.JSON(200, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data: map[string]interface{}{
				"task_id": req.TaskID,
				"status":  "stopping",
			},
			TraceID: tid.(string),
		})
	}
}

func (m *AutoPublishManager) FinishAutoPublish() gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)

		var req model.AutoPublishFinishReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		if req.TaskID == "" || req.UserID == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("task_id 和 user_id 不能为空"))
			return
		}

		m.mu.RLock()
		job, exists := m.jobs[req.TaskID]
		m.mu.RUnlock()

		if exists {
			if job.UserID != req.UserID {
				model.Error(c, model.ErrUnauthorized.WithDetail("无权操作此任务"))
				return
			}

			job.mu.Lock()
			if job.Status == "completed" || job.Status == "stopped" {
				job.mu.Unlock()
				tid, _ := c.Get(model.TraceIDKey)
				c.JSON(200, model.APIResponse{
					Code:    0,
					Message: "ok",
					Data: map[string]interface{}{
						"task_id": req.TaskID,
						"status":  job.Status,
					},
					TraceID: tid.(string),
				})
				return
			}
			job.Status = "finishing"
			job.mu.Unlock()

			select {
			case job.finishCh <- struct{}{}:
			default:
			}

			if logger != nil {
				logger.Info("自动发布已完结: task=%s", req.TaskID)
			}

			tid, _ := c.Get(model.TraceIDKey)
			c.JSON(200, model.APIResponse{
				Code:    0,
				Message: "ok",
				Data: map[string]interface{}{
					"task_id": req.TaskID,
					"status":  "finishing",
				},
				TraceID: tid.(string),
			})
			return
		}

		taskInfo, err := m.fetchTaskInfo(req.TaskID)
		if err != nil {
			if logger != nil {
				logger.Error(logging.ErrNotFound, "完结: 获取任务信息失败: task=%s err=%v", req.TaskID, err)
			}
			model.Error(c, model.ErrNotFound.WithDetail(fmt.Sprintf("任务 %s 不存在", req.TaskID)))
			return
		}

		if taskInfo.UID != "" && taskInfo.UID != req.UserID {
			model.Error(c, model.ErrUnauthorized.WithDetail("无权操作此任务"))
			return
		}

		if logger != nil {
			logger.Info("手动完结: task=%s", req.TaskID)
		}

		go m.executeFinish(req.TaskID, req.UserID, taskInfo)

		tid, _ := c.Get(model.TraceIDKey)
		c.JSON(200, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data: map[string]interface{}{
				"task_id": req.TaskID,
				"status":  "finishing",
			},
			TraceID: tid.(string),
		})
	}
}

// ========== 核心发布循环（May 29 方案：generateChapter） ==========

func (m *AutoPublishManager) autoPublishLoop(job *AutoPublishJob) {
	for {
		select {
		case <-job.stopCtx.Done():
			m.updateJobStatus(job.TaskID, "stopped")
			log.Printf("[auto_publish] task=%s 收到停止信号,退出循环", job.TaskID)
			return
		case <-job.finishCh:
			log.Printf("[auto_publish] task=%s 收到完结信号,生成结局章", job.TaskID)
			if err := m.generateChapter(job, true); err != nil {
				log.Printf("[auto_publish] task=%s 结局章失败: %v", job.TaskID, err)
			}
			m.updateJobStatus(job.TaskID, "completed")
			return
		default:
		}

		if err := m.generateChapter(job, false); err != nil {
			log.Printf("[auto_publish] task=%s 章节生成/发布失败: %v, 1分钟后重试", job.TaskID, err)

			select {
			case <-job.stopCtx.Done():
				m.updateJobStatus(job.TaskID, "stopped")
				return
			case <-job.finishCh:
				log.Printf("[auto_publish] task=%s 失败重试中收到完结信号,生成结局章", job.TaskID)
				if err := m.generateChapter(job, true); err != nil {
					log.Printf("[auto_publish] task=%s 结局章失败: %v", job.TaskID, err)
				}
				m.updateJobStatus(job.TaskID, "completed")
				return
			case <-time.After(1 * time.Minute):
			}

			var pubErr *publishRetryError
			if errors.As(err, &pubErr) {
				log.Printf("[auto_publish] task=%s 仅重试发布步骤 draftItemID=%s chapter=%s", job.TaskID, pubErr.draftItemID, pubErr.chapterTitle)
				if retryErr := m.retryPublishOnly(job, pubErr.sessionID, pubErr.draftItemID, pubErr.chapterTitle, pubErr.volume); retryErr != nil {
					log.Printf("[auto_publish] task=%s 重试发布仍失败: %v, 跳过此章", job.TaskID, retryErr)
				}
			}

			var saveErr *saveDraftRetryError
			if errors.As(err, &saveErr) {
				log.Printf("[auto_publish] task=%s 重试存草稿+发布 chapter=%d title=%s", job.TaskID, saveErr.chapterNum, saveErr.chapterTitle)
				if retryErr := m.retrySaveAndPublish(job, saveErr.sessionID, saveErr.draft, saveErr.chapterTitle, saveErr.chapterNum, saveErr.volume); retryErr != nil {
					log.Printf("[auto_publish] task=%s 重试存草稿+发布仍失败: %v, 跳过此章", job.TaskID, retryErr)
				}
			}
			continue
		}

		select {
		case <-job.stopCtx.Done():
			m.updateJobStatus(job.TaskID, "stopped")
			log.Printf("[auto_publish] task=%s 收到停止信号,退出循环", job.TaskID)
			return
		case <-job.finishCh:
			log.Printf("[auto_publish] task=%s 收到完结信号,生成结局章", job.TaskID)
			if err := m.generateChapter(job, true); err != nil {
				log.Printf("[auto_publish] task=%s 结局章失败: %v", job.TaskID, err)
			}
			m.updateJobStatus(job.TaskID, "completed")
			return
		default:
		}

		time.Sleep(2 * time.Second)
	}
}

// generateChapter May 29 方案核心：先查平台状态确定章号 → 创作 → 推草稿箱。
func (m *AutoPublishManager) generateChapter(job *AutoPublishJob, isFinale bool) error {
	if m.fanqieAdapter == nil {
		return fmt.Errorf("fanqie adapter not configured")
	}

	job.mu.Lock()
	novelName := job.NovelName
	job.mu.Unlock()

	cred, err := m.getFanqieCredential(job)
	if err != nil {
		return fmt.Errorf("credential: %w", err)
	}

	taskID := job.TaskID

	log.Printf("[auto_publish] task=%s ===== 开始生成章节 (isFinale=%v) =====", taskID, isFinale)

	// ① 查平台草稿箱 + 已发布列表
	platformInfo, pubErr := m.fanqieAdapter.GetPlatformInfo(job.ctx(), novelName, cred, job.WorkID)
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

	// ② 确定下一章号
	job.mu.Lock()
	currentVolume := job.VolumeName
	currentChapter := job.ChapterNumber
	job.mu.Unlock()

	if currentVolume == "" {
		currentVolume = "第一卷"
	}

	nextChapter, nextVolume := m.determineNextChapter(lastPublished, currentVolume, currentChapter, platformInfo)
	log.Printf("[auto_publish] task=%s 计算下一章: volume=%s chapter=%d (lastPublished=%d currentChapter=%d)",
		taskID, nextVolume, nextChapter, lastPublished.ChapterNumber, currentChapter)

	if m.isAlreadyPublished(lastPublished, nextChapter) {
		log.Printf("[auto_publish] task=%s 章节 %d 已在已发布列表中，跳过生成，直接推进号", taskID, nextChapter)
		job.mu.Lock()
		job.ChapterNumber = nextChapter
		job.VolumeName = nextVolume
		job.mu.Unlock()
		m.updateTaskChapterNumber(job, "", nextChapter)
		return nil
	}

	// ③ 创作章节
	log.Printf("[auto_publish] task=%s AI 生成章节 chapter=%d vol=%s", taskID, nextChapter, nextVolume)

	job.mu.Lock()
	oldVolume := job.VolumeName
	oldChapter := job.ChapterNumber
	job.VolumeName = nextVolume
	job.ChapterNumber = nextChapter
	job.mu.Unlock()

	sessionID, _, err := m.wakeTask(job, isFinale)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "already has active session") || strings.Contains(errStr, "active session") {
			log.Printf("[auto_publish] task=%s 存在活跃session，尝试关闭后重试", taskID)
			existingSID := m.extractSessionFromError(errStr)
			if existingSID == "" {
				sessions, fetchErr := m.fetchSessions(taskID)
				if fetchErr == nil && len(sessions) > 0 {
					existingSID = sessions[0].SessionID
				}
			}
			if existingSID != "" {
				m.closeSessionQuiet(existingSID)
				log.Printf("[auto_publish] task=%s 已关闭旧session=%s，重试wake", taskID, existingSID)
				sessionID, _, err = m.wakeTask(job, isFinale)
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

	draft, chapterTitle, draftVersion, err := m.waitForSession(job, sessionID)
	if err != nil {
		m.closeSessionQuiet(sessionID)
		return fmt.Errorf("wait for session: %w", err)
	}
	m.closeSessionQuiet(sessionID)

	job.mu.Lock()
	job.DraftVersion = draftVersion
	job.mu.Unlock()

	log.Printf("[auto_publish] task=%s AI 生成完成: title=%s contentLen=%d", taskID, chapterTitle, len(draft))

	if chapterTitle == "" {
		chapterTitle = fallbackChapterTitle(draft)
		log.Printf("[auto_publish] task=%s 标题为空，从正文生成兜底标题: %s", taskID, chapterTitle)
	}

	// ④ 推到草稿箱
	log.Printf("[auto_publish] task=%s 存草稿到平台草稿箱 title=%s chapter=%d", taskID, chapterTitle, nextChapter)

	saveResult := m.fanqieAdapter.SaveDraft(job.ctx(), chapterTitle, draft, novelName, nextChapter, cred, job.WorkID)
	if saveResult.Status != "ok" {
		if saveResult.ErrorCode == c1.ErrCodeDailyLimit {
			return fmt.Errorf("save draft: DAILY_LIMIT: %s", saveResult.ErrorMessage)
		}
		return &saveDraftRetryError{
			sessionID:    sessionID,
			draft:        draft,
			chapterTitle: chapterTitle,
			chapterNum:   nextChapter,
			volume:       nextVolume,
			err:          fmt.Errorf("save draft: %s (code=%s)", saveResult.ErrorMessage, saveResult.ErrorCode),
		}
	}
	log.Printf("[auto_publish] task=%s 存草稿成功: title=%s", taskID, chapterTitle)

	m.updateTaskChapterNumber(job, chapterTitle, nextChapter)

	// ⑤ 从草稿箱推发布
	log.Printf("[auto_publish] task=%s 从草稿箱推发布 title=%s chapter=%d", taskID, chapterTitle, nextChapter)

	platformInfo2, pubErr2 := m.fanqieAdapter.GetPlatformInfo(job.ctx(), novelName, cred, job.WorkID)
	var draftItemID string
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

		if isNewBook {
		log.Printf("[auto_publish] task=%s 检测到新书, 开始设置书籍信息", taskID)
		name, description, category, roles, fetchErr := m.fetchSkillMeta(job.SkillID)
		if fetchErr != nil {
			log.Printf("[auto_publish] task=%s 获取skill元信息失败: %v", taskID, fetchErr)
		} else {
			if platformInfo.BookName != "" {
				name = platformInfo.BookName
			}
			author, authorErr := m.fanqieAdapter.ResolveAuthorName(job.ctx(), cred)
			if authorErr != nil {
				log.Printf("[auto_publish] task=%s 获取账号笔名失败: %v, 使用novelName作为fallback", taskID, authorErr)
				author = novelName
			}
			coverBytes, downloadErr := m.downloadRenderedCover(job.SkillID, author, name)
			if downloadErr != nil {
				log.Printf("[auto_publish] task=%s 下载渲染封面失败: %v", taskID, downloadErr)
			} else {
				result := m.fanqieAdapter.SetBookInfo(job.ctx(), cred, platformInfo.WorkID, name, description, category, roles, coverBytes)
				if result.Status != "ok" {
					log.Printf("[auto_publish] task=%s 设置书籍信息失败: %s (code=%s)", taskID, result.ErrorMessage, result.ErrorCode)
				} else {
					log.Printf("[auto_publish] task=%s 书籍信息设置成功: name=%s", taskID, name)
				}
			}
		}
	}

	pubResult := m.fanqieAdapter.PublishDraft(job.ctx(), chapterTitle, novelName, nextVolume, cred, job.WorkID, draftItemID)
	if pubResult.Status != "ok" {
		log.Printf("[auto_publish] task=%s 发布草稿失败: %s (code=%s)", taskID, pubResult.ErrorMessage, pubResult.ErrorCode)
		return &publishRetryError{
			sessionID:    sessionID,
			draftItemID:  draftItemID,
			chapterTitle: chapterTitle,
			volume:       nextVolume,
			err:          fmt.Errorf("publish draft: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode),
		}
	}

	log.Printf("[auto_publish] task=%s 发布草稿成功: title=%s postId=%s", taskID, chapterTitle, pubResult.PostID)

	if pubResult.PostID != "" && pubResult.PostID != job.WorkID {
		m.updatePublishedCount(job)
		m.saveSessionPostID(job.TaskID, sessionID, pubResult.PostID)
	} else {
		log.Printf("[auto_publish] task=%s postId 无效(workId=%s)，跳过发布计数", taskID, job.WorkID)
	}

	log.Printf("[auto_publish] task=%s ===== 章节生成完成 chapter=%d =====", taskID, nextChapter)
	return nil
}

func (job *AutoPublishJob) ctx() context.Context {
	return job.stopCtx
}

// getFanqieCredential 从 A1 密钥库获取 fanqie 平台的 cookie。
func (m *AutoPublishManager) getFanqieCredential(job *AutoPublishJob) (string, error) {
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

		url := fmt.Sprintf("%s/api/account/credentials", m.a1BaseURL)
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
		resp, err := m.httpClient.Do(req)
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
func (m *AutoPublishManager) determineNextChapter(lastPublished *c1.FanqieLastPublished, currentVolume string, currentChapter int, platformInfo *c1.PlatformInfo) (int, string) {
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
func (m *AutoPublishManager) isAlreadyPublished(lastPublished *c1.FanqieLastPublished, nextChapter int) bool {
	if lastPublished == nil {
		return false
	}
	return nextChapter <= lastPublished.ChapterNumber
}

// wakeTask 创建新的创作 session。
func (m *AutoPublishManager) wakeTask(job *AutoPublishJob, isFinale bool) (string, int, error) {
	url := fmt.Sprintf("%s/api/task/%s/wake", m.sessionMgrURL, job.TaskID)

	job.mu.Lock()
	chapterNum := job.ChapterNumber
	volName := job.VolumeName
	novelName := job.NovelName
	draftVer := job.DraftVersion
	skillID := job.SkillID
	job.mu.Unlock()

	body := map[string]interface{}{
		"is_finale":      isFinale,
		"draft_version":  draftVer,
		"skill_id":       skillID,
		"novel_name":     novelName,
		"volume_name":    volName,
		"chapter_number": chapterNum,
	}

	respBody, err := m.doPost(url, body)
	if err != nil {
		return "", 0, err
	}

	var resp struct {
		SessionID string `json:"session_id"`
		TaskID    string `json:"task_id"`
		Status    string `json:"status"`
		Error     string `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", 0, fmt.Errorf("parse wake response: %w", err)
	}

	if resp.Error != "" {
		return "", 0, fmt.Errorf("wake failed: %s", resp.Error)
	}

	if resp.SessionID == "" {
		return "", 0, fmt.Errorf("empty session_id in wake response")
	}

	return resp.SessionID, chapterNum, nil
}

func (m *AutoPublishManager) waitForSession(job *AutoPublishJob, sessionID string) (string, string, int, error) {
	deadline := time.Now().Add(sessionWaitTimeout)
	ticker := time.NewTicker(sessionPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-job.stopCtx.Done():
			return "", "", 0, fmt.Errorf("auto-publish stopped while waiting for session %s", sessionID)
		case <-ticker.C:
			if time.Now().After(deadline) {
				return "", "", 0, fmt.Errorf("timeout waiting for session %s", sessionID)
			}

			status, draftVersion, err := m.getSessionStatus(sessionID)
			if err != nil {
				log.Printf("[auto_publish] task=%s 获取会话状态失败: %v, 继续等待", job.TaskID, err)
				continue
			}

			if status == "NO_CONTENT" {
				return "", "", 0, fmt.Errorf("session %s produced no content", sessionID)
			}

			if status == "DRAFT_READY" || status == "WARM" || status == "ARCHIVED" || status == "COLD" {
				draft, chapterTitle, err := m.getDraft(sessionID)
				if err != nil {
					return "", "", 0, fmt.Errorf("session %s reached terminal status %s but no draft file: %w", sessionID, status, err)
				}
				return draft, chapterTitle, draftVersion, nil
			}
		}
	}
}

func (m *AutoPublishManager) getSessionStatus(sessionID string) (string, int, error) {
	url := fmt.Sprintf("%s/api/session/%s", m.sessionMgrURL, sessionID)
	respBody, err := m.doGet(url)
	if err != nil {
		return "", 0, err
	}

	var resp struct {
		Status       string `json:"status"`
		DraftVersion int    `json:"draft_version"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", 0, fmt.Errorf("parse session status: %w", err)
	}

	return resp.Status, resp.DraftVersion, nil
}

func (m *AutoPublishManager) getDraft(sessionID string) (string, string, error) {
	url := fmt.Sprintf("%s/api/session/%s/draft", m.sessionMgrURL, sessionID)
	respBody, err := m.doGet(url)
	if err != nil {
		return "", "", err
	}

	var resp struct {
		Draft        string `json:"draft"`
		ChapterTitle string `json:"chapter_title"`
	}

	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", "", fmt.Errorf("parse draft response: %w", err)
	}

	return resp.Draft, resp.ChapterTitle, nil
}

func (m *AutoPublishManager) closeSessionQuiet(sessionID string) {
	url := fmt.Sprintf("%s/api/session/%s/close", m.sessionMgrURL, sessionID)
	_, err := m.doPost(url, map[string]interface{}{})
	if err != nil {
		log.Printf("[auto_publish] 关闭会话失败 session=%s: %v", sessionID, err)
	}
}

func (m *AutoPublishManager) extractSessionFromError(errMsg string) string {
	idx := strings.Index(errMsg, "active session ")
	if idx < 0 {
		return ""
	}
	rest := errMsg[idx+len("active session "):]
	end := strings.IndexFunc(rest, func(r rune) bool {
		return r == ' ' || r == '\n' || r == ','
	})
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func (m *AutoPublishManager) fetchSessions(taskID string) ([]sessionRaw, error) {
	url := fmt.Sprintf("%s/api/task/%s/sessions", m.sessionMgrURL, taskID)
	respBody, err := m.doGet(url)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Sessions []sessionRaw `json:"sessions"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

type sessionRaw struct {
	SessionID     string `json:"session_id"`
	Status        string `json:"status"`
	ChapterNumber int    `json:"chapter_number"`
}

// publishRetryError 表示 PublishDraft 失败但草稿已保存，可只重试发布步骤
type publishRetryError struct {
	sessionID    string
	draftItemID  string
	chapterTitle string
	volume       string
	err          error
}

func (e *publishRetryError) Error() string {
	return e.err.Error()
}

// saveDraftRetryError 表示 SaveDraft 失败，AI 草稿已生成，可重试存草稿+发布
type saveDraftRetryError struct {
	sessionID    string
	draft        string
	chapterTitle string
	chapterNum   int
	volume       string
	err          error
}

func (e *saveDraftRetryError) Error() string {
	return e.err.Error()
}

var fallbackTitlePunctRe = regexp.MustCompile(`[，,。、；;：:！!？?…""''""【】（）()《》—\-~～\s]+`)

func fallbackChapterTitle(draft string) string {
	lines := strings.SplitN(draft, "\n", 30)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cleaned := fallbackTitlePunctRe.ReplaceAllString(trimmed, "")
		runes := []rune(cleaned)
		if len(runes) == 0 {
			continue
		}
		if len(runes) > 8 {
			return string(runes[:8])
		}
		return string(runes)
	}
	return ""
}

func (m *AutoPublishManager) updateTaskChapterNumber(job *AutoPublishJob, chapterTitle string, chapterNumber int) {
	url := fmt.Sprintf("%s/api/task/%s/update", m.sessionMgrURL, job.TaskID)

	body := map[string]interface{}{
		"novel_name":     job.NovelName,
		"volume_name":    job.VolumeName,
		"title":          chapterTitle,
		"chapter_number": chapterNumber,
	}

	respBody, err := m.doPost(url, body)
	if err != nil {
		log.Printf("[auto_publish] task=%s 更新章节号失败: %v", job.TaskID, err)
		return
	}
	log.Printf("[auto_publish] task=%s 章节号已推进至%d: %s", job.TaskID, chapterNumber, string(respBody))
}

func (m *AutoPublishManager) updatePublishedCount(job *AutoPublishJob) {
	url := fmt.Sprintf("%s/api/task/%s/update", m.sessionMgrURL, job.TaskID)
	body := map[string]interface{}{
		"chapter_count_delta": 1,
	}
	respBody, err := m.doPost(url, body)
	if err != nil {
		log.Printf("[auto_publish] task=%s 更新已发布章数失败: %v", job.TaskID, err)
		return
	}
	log.Printf("[auto_publish] task=%s 已发布章数已递增: %s", job.TaskID, string(respBody))
}

func (m *AutoPublishManager) saveSessionPostID(taskID, sessionID, postID string) {
	url := fmt.Sprintf("%s/api/task/%s/update", m.sessionMgrURL, taskID)
	body := map[string]interface{}{
		"session_id": sessionID,
		"post_id":    postID,
	}
	respBody, err := m.doPost(url, body)
	if err != nil {
		log.Printf("[auto_publish] task=%s 保存PostID失败 session=%s err=%v", taskID, sessionID, err)
		return
	}
	log.Printf("[auto_publish] task=%s 保存PostID成功 session=%s resp=%s", taskID, sessionID, string(respBody))
}

func (m *AutoPublishManager) retryPublishOnly(job *AutoPublishJob, sessionID, draftItemID, chapterTitle, volume string) error {
	cred, err := m.getFanqieCredential(job)
	if err != nil {
		return fmt.Errorf("credential: %w", err)
	}

	log.Printf("[auto_publish] task=%s 重试发布: draftItemID=%s chapter=%s volume=%s", job.TaskID, draftItemID, chapterTitle, volume)

	pubResult := m.fanqieAdapter.PublishDraft(job.ctx(), chapterTitle, job.NovelName, volume, cred, job.WorkID, draftItemID)
	if pubResult.Status != "ok" {
		return fmt.Errorf("publish draft retry: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode)
	}

	log.Printf("[auto_publish] task=%s 重试发布成功: title=%s postId=%s", job.TaskID, chapterTitle, pubResult.PostID)

	if pubResult.PostID != "" && pubResult.PostID != job.WorkID {
		m.updatePublishedCount(job)
		m.saveSessionPostID(job.TaskID, sessionID, pubResult.PostID)
	} else {
		log.Printf("[auto_publish] task=%s 重试发布 postId 无效(workId=%s)，跳过发布计数", job.TaskID, job.WorkID)
	}
	return nil
}

func (m *AutoPublishManager) retrySaveAndPublish(job *AutoPublishJob, sessionID, draft, chapterTitle string, chapterNum int, volume string) error {
	cred, err := m.getFanqieCredential(job)
	if err != nil {
		return fmt.Errorf("credential: %w", err)
	}

	novelName := job.NovelName
	log.Printf("[auto_publish] task=%s 重试存草稿+发布: chapter=%d title=%s volume=%s", job.TaskID, chapterNum, chapterTitle, volume)

	saveResult := m.fanqieAdapter.SaveDraft(job.ctx(), chapterTitle, draft, novelName, chapterNum, cred, job.WorkID)
	if saveResult.Status != "ok" {
		return fmt.Errorf("save draft retry: %s (code=%s)", saveResult.ErrorMessage, saveResult.ErrorCode)
	}
	log.Printf("[auto_publish] task=%s 重试存草稿成功: title=%s", job.TaskID, chapterTitle)

	m.updateTaskChapterNumber(job, chapterTitle, chapterNum)

	platformInfo, pubErr := m.fanqieAdapter.GetPlatformInfo(job.ctx(), novelName, cred, job.WorkID)
	var draftItemID string
	if pubErr == nil {
		for _, d := range platformInfo.Drafts {
			if d.ChapterNumber == chapterNum {
				draftItemID = d.ItemID
				break
			}
		}
	}

	pubResult := m.fanqieAdapter.PublishDraft(job.ctx(), chapterTitle, novelName, volume, cred, job.WorkID, draftItemID)
	if pubResult.Status != "ok" {
		return fmt.Errorf("publish draft retry: %s (code=%s)", pubResult.ErrorMessage, pubResult.ErrorCode)
	}

	log.Printf("[auto_publish] task=%s 重试发布成功: title=%s postId=%s", job.TaskID, chapterTitle, pubResult.PostID)

	if pubResult.PostID != "" && pubResult.PostID != job.WorkID {
		m.updatePublishedCount(job)
		m.saveSessionPostID(job.TaskID, sessionID, pubResult.PostID)
	}
	return nil
}

func (m *AutoPublishManager) executeFinish(taskID, userID string, taskInfo *taskInfoData) {
	skillID := taskInfo.SkillID
	if skillID == "" {
		skillID = "general_fallback_v1"
	}

	stopCtx, stopCancel := context.WithCancel(context.Background())
	job := &AutoPublishJob{
		TaskID:        taskID,
		UserID:        userID,
		Platform:      taskInfo.Platform,
		Accounts:      nil,
		SkillID:       skillID,
		Topic:         taskInfo.Topic,
		NovelName:     taskInfo.NovelName,
		VolumeName:    taskInfo.VolumeName,
		ChapterNumber: taskInfo.ChapterNumber,
		DraftVersion:  taskInfo.SessionCount,
		Status:        "finishing",
		stopCtx:       stopCtx,
		stopCancel:    stopCancel,
		finishCh:      make(chan struct{}, 1),
		createdAt:     time.Now(),
	}

	m.mu.Lock()
	m.jobs[taskID] = job
	m.mu.Unlock()

	if err := m.generateChapter(job, true); err != nil {
		log.Printf("[auto_publish] task=%s 手动完结失败: %v", taskID, err)
		m.updateJobStatus(taskID, "stopped")
		return
	}

	m.updateJobStatus(taskID, "completed")
	log.Printf("[auto_publish] task=%s 手动完结完成", taskID)
}

// ========== 辅助方法 ==========

func (m *AutoPublishManager) fetchTaskInfo(taskID string) (*taskInfoData, error) {
	url := fmt.Sprintf("%s/api/task/%s", m.sessionMgrURL, taskID)
	respBody, err := m.doGet(url)
	if err != nil {
		return nil, err
	}

	var info taskInfoData
	if err := json.Unmarshal(respBody, &info); err != nil {
		return nil, fmt.Errorf("parse task info: %w", err)
	}

	return &info, nil
}

func (m *AutoPublishManager) updateJobStatus(taskID, status string) {
	m.mu.RLock()
	job, exists := m.jobs[taskID]
	m.mu.RUnlock()
	if exists {
		job.mu.Lock()
		job.Status = status
		job.mu.Unlock()
	}
}

func (m *AutoPublishManager) doPost(url string, body interface{}) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (m *AutoPublishManager) doGet(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (m *AutoPublishManager) fetchSkillMeta(skillID string) (name, description, category, roles string, err error) {
	url := fmt.Sprintf("%s/api/skill/%s", m.skillRegistryURL, skillID)
	respBody, err := m.doGet(url)
	if err != nil {
		return "", "", "", "", fmt.Errorf("fetch skill meta: %w", err)
	}
	var meta struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Roles       string `json:"roles"`
	}
	if err := json.Unmarshal(respBody, &meta); err != nil {
		return "", "", "", "", fmt.Errorf("parse skill meta: %w", err)
	}
	log.Printf("[auto_publish] fetchSkillMeta: skill=%s name=%s category=%s roles=%s", skillID, meta.Name, meta.Category, meta.Roles)
	return meta.Name, meta.Description, meta.Category, meta.Roles, nil
}

func (m *AutoPublishManager) downloadRenderedCover(skillID, author, name string) ([]byte, error) {
	queryURL := fmt.Sprintf("%s/api/skill/%s/cover-rendered?author=%s&name=%s",
		m.skillRegistryURL, skillID, url.QueryEscape(author), url.QueryEscape(name))
	respBody, err := m.doGet(queryURL)
	if err != nil {
		return nil, fmt.Errorf("download rendered cover: %w", err)
	}
	log.Printf("[auto_publish] downloadRenderedCover: skill=%s size=%d bytes", skillID, len(respBody))
	return respBody, nil
}

// ========== 卷管理 ==========

var volumeNumMap = map[string]int{
	"第一卷": 1, "第二卷": 2, "第三卷": 3, "第四卷": 4, "第五卷": 5,
	"第六卷": 6, "第七卷": 7, "第八卷": 8, "第九卷": 9, "第十卷": 10,
}

var volumeNameMap = map[int]string{
	1: "第一卷", 2: "第二卷", 3: "第三卷", 4: "第四卷", 5: "第五卷",
	6: "第六卷", 7: "第七卷", 8: "第八卷", 9: "第九卷", 10: "第十卷",
}

func volumeCapacity(volNum int) int {
	return 300 + 50*volNum
}

func (m *AutoPublishManager) trySwitchVolume(job *AutoPublishJob, chapterNum int) {
	if chapterNum < volumeCapacity(volumeNumMap[job.VolumeName]) {
		return
	}

	volNum := volumeNumMap[job.VolumeName]
	if volNum <= 0 {
		return
	}
	nextVolNum := volNum + 1
	nextVolName := volumeNameMap[nextVolNum]
	if nextVolName == "" {
		return
	}

	job.mu.Lock()
	job.VolumeName = nextVolName
	job.ChapterNumber = 0
	nextVol := job.VolumeName
	job.mu.Unlock()

	log.Printf("[auto_publish] task=%s 卷切换: %s -> %s, 章号重置为1", job.TaskID, volumeNameMap[volNum], nextVol)

	url := fmt.Sprintf("%s/api/task/%s/update", m.sessionMgrURL, job.TaskID)
	body := map[string]interface{}{
		"volume_name":    nextVol,
		"chapter_number": 0,
	}
	_, err := m.doPost(url, body)
	if err != nil {
		log.Printf("[auto_publish] task=%s 卷切换持久化失败: %v", job.TaskID, err)
	}
}

type taskInfoData struct {
	TaskID                string `json:"task_id"`
	UID                   string `json:"uid"`
	Topic                 string `json:"topic"`
	Platform              string `json:"platform"`
	SkillID               string `json:"skill_id"`
	NovelName             string `json:"novel_name"`
	VolumeName            string `json:"volume_name"`
	ChapterNumber         int    `json:"chapter_number"`
	SessionCount          int    `json:"session_count"`
	PublishedChapterCount int    `json:"published_chapter_count"`
}
