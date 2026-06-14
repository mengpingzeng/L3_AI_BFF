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

var ErrDailyLimitReached = errors.New("daily_limit_reached")

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
	platforms         map[string]NovelPlatform
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
	retryCount    int
	BookInfoSet   bool
	onExit        func(job *AutoPublishJob, newStatus string)
	onExitRequeue func(job *AutoPublishJob, err error)
}

func NewAutoPublishManager(sessionMgrURL, workflowURL, accountURL, skillRegistryURL, stoppedTasksFile string, platforms map[string]NovelPlatform, a1BaseURL string) *AutoPublishManager {
	m := &AutoPublishManager{
		jobs:             make(map[string]*AutoPublishJob),
		sessionMgrURL:    sessionMgrURL,
		workflowURL:      workflowURL,
		accountURL:       accountURL,
		skillRegistryURL: skillRegistryURL,
		stoppedTasksFile: stoppedTasksFile,
		stoppedTasks:     make(map[string]bool),
		platforms:        platforms,
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

// ========== 核心发布循环（v7：写/存/发三阶段独立重试） ==========

type chapterGenState struct {
	draft         string
	chapterTitle  string
	sessionID     string
	chapterNumber int
	volume        string
	apiVolumeName string
	volumeId      string
	draftItemID   string
	fullTitle     string
	platformInfo  *c1.PlatformInfo
	cred          string
}

func (m *AutoPublishManager) autoPublishLoop(job *AutoPublishJob) {
	platform, ok := m.platforms[job.Platform]
	if !ok {
		log.Printf("[auto_publish] task=%s unsupported platform=%s", job.TaskID, job.Platform)
		m.cleanupSessions(job)
		m.updateJobStatus(job.TaskID, "stopped")
		if job.onExit != nil {
			job.onExit(job, "stopped")
		}
		return
	}
	platform.Run(job)
}

func (m *AutoPublishManager) sleepOrStop(job *AutoPublishJob, d time.Duration) {
	select {
	case <-job.stopCtx.Done():
	case <-time.After(d):
	}
}

func (m *AutoPublishManager) exitWithRequeue(job *AutoPublishJob, err error) {
	m.cleanupSessions(job)
	if job.onExitRequeue != nil {
		job.onExitRequeue(job, err)
	}
}

func (m *AutoPublishManager) exitWithStop(job *AutoPublishJob) {
	m.cleanupSessions(job)
	m.updateJobStatus(job.TaskID, "stopped")
	if job.onExit != nil {
		job.onExit(job, "stopped")
	}
}

func (m *AutoPublishManager) exitWithDailyLimit(job *AutoPublishJob) {
	m.cleanupSessions(job)
	if job.onExitRequeue != nil {
		job.onExitRequeue(job, ErrDailyLimitReached)
	}
}

// phaseGenerate 阶段二：AI生成章节内容（wakeTask + waitForSession + getDraft）。
// 失败时可以重试——会重新创建session生成新内容。
func (m *AutoPublishManager) phaseGenerate(job *AutoPublishJob, state *chapterGenState) error {
	taskID := job.TaskID
	log.Printf("[auto_publish] task=%s AI生成章节 chapter=%d vol=%s", taskID, state.chapterNumber, state.volume)

	m.cleanupExistingChapter(taskID, state.volume, state.chapterNumber)

	sessionID, _, err := m.wakeTask(job, false)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "already has active session") || strings.Contains(errStr, "active session") {
			existingSID := m.extractSessionFromError(errStr)
			if existingSID == "" {
				sessions, fetchErr := m.fetchSessions(taskID)
				if fetchErr == nil && len(sessions) > 0 {
					existingSID = sessions[0].SessionID
				}
			}
			if existingSID != "" {
				if m.isSessionAlive(existingSID) {
					log.Printf("[auto_publish] task=%s 活跃session=%s 仍在运行中，不关闭，待其自行完成", taskID, existingSID)
				} else {
					log.Printf("[auto_publish] task=%s session=%s 为僵尸，关闭后重试wake", taskID, existingSID)
					m.closeSessionQuiet(existingSID)
					sessionID, _, err = m.wakeTask(job, false)
				}
			}
		}
		if err != nil {
			return fmt.Errorf("wake task: %w", err)
		}
	}

	state.sessionID = sessionID
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

	log.Printf("[auto_publish] task=%s AI生成完成: title=%s contentLen=%d", taskID, chapterTitle, len(draft))

	if chapterTitle == "" {
		chapterTitle = fallbackChapterTitle(draft)
		log.Printf("[auto_publish] task=%s 标题为空，从正文生成兜底标题: %s", taskID, chapterTitle)
	}

	state.draft = draft
	state.chapterTitle = chapterTitle
	state.fullTitle = fmt.Sprintf("第%d章 %s", state.chapterNumber, chapterTitle)
	return nil
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

func (m *AutoPublishManager) cleanupExistingChapter(taskID, volumeName string, chapterNumber int) {
	sessions, err := m.fetchSessions(taskID)
	if err != nil {
		log.Printf("[auto_publish] task=%s cleanupExistingChapter 获取sessions失败: %v", taskID, err)
		return
	}

	var staleIDs []string
	for _, s := range sessions {
		volName := s.VolumeName
		if volName == "" {
			volName = "第一卷"
		}
		if volName == volumeName && s.ChapterNumber == chapterNumber && s.PostID == "" {
			staleIDs = append(staleIDs, s.SessionID)
		}
	}

	if len(staleIDs) == 0 {
		return
	}

	log.Printf("[auto_publish] task=%s cleanupExistingChapter vol=%s ch=%d 清理%d个残留session",
		taskID, volumeName, chapterNumber, len(staleIDs))

	for _, sid := range staleIDs {
		req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/session/%s", m.sessionMgrURL, sid), nil)
		if err != nil {
			log.Printf("[auto_publish] cleanupExistingChapter 构建请求失败 sid=%s: %v", sid, err)
			continue
		}
		resp, err := m.httpClient.Do(req)
		if err != nil {
			log.Printf("[auto_publish] cleanupExistingChapter 请求失败 sid=%s: %v", sid, err)
			continue
		}
		resp.Body.Close()
		log.Printf("[auto_publish] cleanupExistingChapter 已删除残留session=%s", sid)
	}
}

func (m *AutoPublishManager) isSessionAlive(sessionID string) bool {
	url := fmt.Sprintf("%s/api/session/%s/alive", m.sessionMgrURL, sessionID)
	respBody, err := m.doGet(url)
	if err != nil {
		log.Printf("[auto_publish] 查询session存活失败 session=%s: %v", sessionID, err)
		return false
	}
	var resp struct {
		Alive bool `json:"alive"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return false
	}
	return resp.Alive
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
	VolumeName    string `json:"volume_name,omitempty"`
	PostID        string `json:"post_id,omitempty"`
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

	platform, ok := m.platforms[taskInfo.Platform]
	if !ok {
		log.Printf("[auto_publish] task=%s unsupported platform=%s for finalize", taskID, taskInfo.Platform)
		m.updateJobStatus(taskID, "stopped")
		return
	}

	if err := platform.Finalize(job); err != nil {
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

func (m *AutoPublishManager) cleanupSessions(job *AutoPublishJob) {
	sessions, err := m.fetchSessions(job.TaskID)
	if err != nil {
		log.Printf("[auto_publish] task=%s 退出清理: 获取session列表失败: %v", job.TaskID, err)
		return
	}
	for _, s := range sessions {
		if s.Status != "ARCHIVED" && s.Status != "COLD" {
			m.closeSessionQuiet(s.SessionID)
			log.Printf("[auto_publish] task=%s 退出清理 session=%s (status=%s)", job.TaskID, s.SessionID, s.Status)
		}
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
