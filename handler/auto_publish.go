package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/pkg/validator"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

const (
	sessionPollInterval = 3 * time.Second
	sessionWaitTimeout  = 15 * time.Minute
)

type AutoPublishManager struct {
	jobs          map[string]*AutoPublishJob
	mu            sync.RWMutex
	sessionMgrURL string
	workflowURL   string
	accountURL    string
	httpClient    *http.Client
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
	stopCh        chan struct{}
	finishCh      chan struct{}
	mu            sync.Mutex
	createdAt     time.Time
}

func NewAutoPublishManager(sessionMgrURL, workflowURL, accountURL string) *AutoPublishManager {
	return &AutoPublishManager{
		jobs:          make(map[string]*AutoPublishJob),
		sessionMgrURL: sessionMgrURL,
		workflowURL:   workflowURL,
		accountURL:    accountURL,
		httpClient: &http.Client{
			Timeout: 600 * time.Second,
		},
	}
}

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

		m.mu.RLock()
		existing, exists := m.jobs[req.TaskID]
		m.mu.RUnlock()
		if exists {
			existing.mu.Lock()
			status := existing.Status
			existing.mu.Unlock()
			if status == "running" || status == "finishing" {
				model.Error(c, model.ErrConflict.WithDetail(fmt.Sprintf("任务 %s 已有自动发布在运行中", req.TaskID)))
				return
			}
		}

		taskInfo, err := m.fetchTaskInfo(req.TaskID)
		if err != nil {
			if logger != nil {
				logger.Error(logging.ErrNotFound, "自动发布: 获取任务信息失败: task=%s err=%v", req.TaskID, err)
			}
			model.Error(c, model.ErrNotFound.WithDetail(fmt.Sprintf("任务 %s 不存在", req.TaskID)))
			return
		}

		platform := req.Platform
		if platform == "" {
			platform = taskInfo.Platform
		}

		var accounts []map[string]string
		if len(req.Accounts) > 0 {
			uidForLookup := uid
			if roleVal == "admin" {
				uidForLookup = ""
			}
			allUserAccounts := fetchUserAccounts(m.accountURL, uidForLookup, platform)
			userAccountSet := make(map[string]string)
			for _, a := range allUserAccounts {
				userAccountSet[a.AccountID] = a.Platform
			}
			for _, accID := range req.Accounts {
				accPlatform, ok := userAccountSet[accID]
				if roleVal != "admin" && !ok {
					model.Error(c, model.ErrInvalidParam.WithDetail(
						fmt.Sprintf("账号 %s 不属于当前用户或未绑定", accID)))
					return
				}
				if !ok {
					accPlatform = platform
				}
				accounts = append(accounts, map[string]string{
					"accountId": accID,
					"uid":       uid,
					"platform":  accPlatform,
				})
				if platform == "" {
					platform = accPlatform
				}
			}
		} else {
			uidForLookup := uid
			if roleVal == "admin" {
				uidForLookup = ""
			}
			realAccounts := fetchUserAccounts(m.accountURL, uidForLookup, platform)
			if len(realAccounts) == 0 {
				model.Error(c, model.ErrInvalidParam.WithDetail(
					fmt.Sprintf("没有绑定 %s 平台的账号", platform)))
				return
			}
			for _, a := range realAccounts {
				accounts = append(accounts, map[string]string{
					"accountId": a.AccountID,
					"uid":       uid,
					"platform":  a.Platform,
				})
			}
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
			stopCh:        make(chan struct{}),
			finishCh:      make(chan struct{}, 1),
			createdAt:     time.Now(),
		}

		m.mu.Lock()
		m.jobs[req.TaskID] = job
		m.mu.Unlock()

		if logger != nil {
			logger.Info("自动发布已启动: task=%s platform=%s skill=%s", req.TaskID, platform, skillID)
		}

		go m.autoPublishLoop(job)

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

		select {
		case job.stopCh <- struct{}{}:
		default:
		}

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

func (m *AutoPublishManager) autoPublishLoop(job *AutoPublishJob) {
	for {
		select {
		case <-job.stopCh:
			m.updateJobStatus(job.TaskID, "stopped")
			log.Printf("[auto_publish] task=%s 收到停止信号,退出循环", job.TaskID)
			return
		case <-job.finishCh:
			log.Printf("[auto_publish] task=%s 收到完结信号,生成结局章", job.TaskID)
			if err := m.generateAndPublishChapter(job, true); err != nil {
				log.Printf("[auto_publish] task=%s 结局章失败: %v", job.TaskID, err)
			}
			m.updateJobStatus(job.TaskID, "completed")
			return
		default:
		}

		if err := m.generateAndPublishChapter(job, false); err != nil {
			log.Printf("[auto_publish] task=%s 章节生成/发布失败: %v, 1分钟后重试", job.TaskID, err)
			select {
			case <-job.stopCh:
				m.updateJobStatus(job.TaskID, "stopped")
				return
			case <-job.finishCh:
				log.Printf("[auto_publish] task=%s 失败重试中收到完结信号,生成结局章", job.TaskID)
				if err := m.generateAndPublishChapter(job, true); err != nil {
					log.Printf("[auto_publish] task=%s 结局章失败: %v", job.TaskID, err)
				}
				m.updateJobStatus(job.TaskID, "completed")
				return
			case <-time.After(1 * time.Minute):
			}
			continue
		}

		select {
		case <-job.stopCh:
			m.updateJobStatus(job.TaskID, "stopped")
			log.Printf("[auto_publish] task=%s 收到停止信号,退出循环", job.TaskID)
			return
		case <-job.finishCh:
			log.Printf("[auto_publish] task=%s 收到完结信号,生成结局章", job.TaskID)
			if err := m.generateAndPublishChapter(job, true); err != nil {
				log.Printf("[auto_publish] task=%s 结局章失败: %v", job.TaskID, err)
			}
			m.updateJobStatus(job.TaskID, "completed")
			return
		default:
		}

		time.Sleep(2 * time.Second)
	}
}

func (m *AutoPublishManager) generateAndPublishChapter(job *AutoPublishJob, isFinale bool) error {
	sessionID, chapterNum, err := m.wakeTask(job, isFinale)
	if err != nil {
		return fmt.Errorf("wake task: %w", err)
	}
	log.Printf("[auto_publish] task=%s session=%s 已创建,等待生成完成", job.TaskID, sessionID)

	draft, chapterTitle, draftVersion, err := m.waitForSession(job, sessionID)
	if err != nil {
		m.closeSessionQuiet(sessionID)
		return fmt.Errorf("wait for session: %w", err)
	}

	m.closeSessionQuiet(sessionID)

	job.mu.Lock()
	job.DraftVersion = draftVersion
	if chapterNum > 0 {
		job.ChapterNumber = chapterNum
	}
	job.mu.Unlock()

	if err := m.publishChapter(job, sessionID, draft, chapterTitle, chapterNum); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	m.updateTaskMeta(job, chapterTitle, chapterNum)

	m.trySwitchVolume(job, chapterNum)

	return nil
}

func (m *AutoPublishManager) wakeTask(job *AutoPublishJob, isFinale bool) (string, int, error) {
	url := fmt.Sprintf("%s/api/task/%s/wake", m.sessionMgrURL, job.TaskID)

	job.mu.Lock()
	chapterNum := job.ChapterNumber + 1
	volName := job.VolumeName
	novelName := job.NovelName
	draftVer := job.DraftVersion
	job.mu.Unlock()

	body := map[string]interface{}{
		"is_finale":      isFinale,
		"draft_version":  draftVer,
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
		case <-job.stopCh:
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

			if status == "DRAFT_READY" || status == "WARM" || status == "ARCHIVED" || status == "COLD" {
				draft, chapterTitle, err := m.getDraft(sessionID)
				if err != nil {
					log.Printf("[auto_publish] task=%s 获取草稿失败: %v, 继续等待", job.TaskID, err)
					continue
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

func (m *AutoPublishManager) publishChapter(job *AutoPublishJob, sessionID, draft, chapterTitle string, chapterNumber int) error {
	downstream := map[string]interface{}{
		"draftVersion":  job.DraftVersion,
		"sessionId":     sessionID,
		"platform":      job.Platform,
		"accounts":      job.Accounts,
		"skillId":       job.SkillID,
		"topic":         job.Topic,
		"taskId":        job.TaskID,
		"novelName":     job.NovelName,
		"title":         chapterTitle,
		"volumeName":    job.VolumeName,
		"chapterNumber": chapterNumber,
		"uid":           job.UserID,
	}

	url := fmt.Sprintf("%s/api/task/%s/publish", m.workflowURL, job.TaskID)

	respBody, err := m.doPost(url, downstream)
	if err != nil {
		return fmt.Errorf("publish request failed: %w", err)
	}

	var publishResp struct {
		Status  string `json:"status"`
		ErrorCode string `json:"errorCode"`
		ErrorMessage string `json:"errorMessage"`
	}

	if err := json.Unmarshal(respBody, &publishResp); err != nil {
		log.Printf("[auto_publish] task=%s 解析发布响应失败: %v, raw=%s", job.TaskID, err, string(respBody))
		return nil
	}

	if publishResp.ErrorCode != "" {
		log.Printf("[auto_publish] task=%s 发布返回错误: code=%s msg=%s", job.TaskID, publishResp.ErrorCode, publishResp.ErrorMessage)
	}

	log.Printf("[auto_publish] task=%s 发布完成 status=%s chapter=%d", job.TaskID, publishResp.Status, chapterNumber)
	return nil
}

func (m *AutoPublishManager) updateTaskMeta(job *AutoPublishJob, chapterTitle string, chapterNumber int) {
	url := fmt.Sprintf("%s/api/task/%s/update", m.sessionMgrURL, job.TaskID)

	body := map[string]interface{}{
		"novel_name":          job.NovelName,
		"volume_name":         job.VolumeName,
		"title":               chapterTitle,
		"chapter_number":      chapterNumber,
		"chapter_count_delta": 1,
	}

	respBody, err := m.doPost(url, body)
	if err != nil {
		log.Printf("[auto_publish] task=%s 更新任务元数据失败: %v", job.TaskID, err)
		return
	}
	log.Printf("[auto_publish] task=%s 更新元数据完成: %s", job.TaskID, string(respBody))
}

func (m *AutoPublishManager) executeFinish(taskID, userID string, taskInfo *taskInfoData) {
	skillID := taskInfo.SkillID
	if skillID == "" {
		skillID = "general_fallback_v1"
	}

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
		stopCh:        make(chan struct{}),
		finishCh:      make(chan struct{}, 1),
		createdAt:     time.Now(),
	}

	m.mu.Lock()
	m.jobs[taskID] = job
	m.mu.Unlock()

	if err := m.generateAndPublishChapter(job, true); err != nil {
		log.Printf("[auto_publish] task=%s 手动完结失败: %v", taskID, err)
		m.updateJobStatus(taskID, "stopped")
		return
	}

	m.updateJobStatus(taskID, "completed")
	log.Printf("[auto_publish] task=%s 手动完结完成", taskID)
}

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
