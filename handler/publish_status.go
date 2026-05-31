package handler

import (
	"encoding/json"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

type publishStatusData struct {
	LatestVolumeName           string `json:"latest_volume_name"`
	LatestChapterNumber        int    `json:"latest_chapter_number"`
	LatestPublishedSessionID   string `json:"latest_published_session_id"`
	ActiveSessionID            string `json:"active_session_id"`
	PublishStatus              string `json:"publish_status"`
	IsAutoPublishRunning       bool   `json:"is_auto_publish_running"`
}

func (m *AutoPublishManager) GetPublishStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)
		taskID := c.Query("task_id")
		if taskID == "" {
			if logger != nil {
				logger.Warn(logging.ErrInvalidParam, "publish/get_status: task_id is empty")
			}
			model.Error(c, model.ErrInvalidParam.WithDetail("task_id is required"))
			return
		}

		if logger != nil {
			logger.Info("publish/get_status: query task=%s", taskID)
		}

		taskData, err := m.doGet(m.sessionMgrURL + "/api/task/" + taskID)
		if err != nil {
			if logger != nil {
				logger.Error(logging.ErrNotFound, "publish/get_status: get task failed: task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrNotFound.WithDetail("任务不存在"))
			return
		}

		var task struct {
			VolumeName      string `json:"volume_name"`
			ActiveSessionID string `json:"active_session_id"`
		}
		if err := json.Unmarshal(taskData, &task); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError, "publish/get_status: parse task failed: task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		wfStatus, wfSessionID, wfChapterNum, wfVolumeName := mapWorkflowStatus(taskID, m)
		if logger != nil {
			logger.Info("publish/get_status: workflow status: task=%s status=%s session=%s chapter=%d volume=%s",
				taskID, wfStatus, wfSessionID, wfChapterNum, wfVolumeName)
		}

		isRunning := m.isAutoPublishRunning(taskID)

		volumeName := wfVolumeName
		if volumeName == "" {
			volumeName = task.VolumeName
		}

		data := publishStatusData{
			LatestVolumeName:         volumeName,
			LatestChapterNumber:      wfChapterNum,
			LatestPublishedSessionID: wfSessionID,
			ActiveSessionID:          task.ActiveSessionID,
			PublishStatus:            wfStatus,
			IsAutoPublishRunning:     isRunning,
		}

		if logger != nil {
			logger.Info("publish/get_status: response task=%s volume=%s chapter=%d session=%s status=%s active_session=%s auto_running=%v",
				taskID, data.LatestVolumeName, data.LatestChapterNumber, data.LatestPublishedSessionID,
				data.PublishStatus, data.ActiveSessionID, data.IsAutoPublishRunning)
		}

		model.Success(c, data)
	}
}

func (m *AutoPublishManager) isAutoPublishRunning(taskID string) bool {
	m.mu.RLock()
	job, exists := m.jobs[taskID]
	m.mu.RUnlock()
	if !exists {
		return false
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.Status == "running" || job.Status == "finishing"
}

func mapWorkflowStatus(taskID string, m *AutoPublishManager) (status string, sessionID string, chapterNum int, volumeName string) {
	respBody, err := m.doGet(m.workflowURL + "/api/task/" + taskID + "/status")
	if err != nil {
		return "idle", "", 0, ""
	}

	var wf struct {
		Status        string `json:"status"`
		SessionID     string `json:"session_id"`
		ChapterNumber int    `json:"chapter_number"`
		VolumeName    string `json:"volume_name"`
		Exists        bool   `json:"exists"`
	}
	if err := json.Unmarshal(respBody, &wf); err != nil {
		return "idle", "", 0, ""
	}
	if !wf.Exists || wf.Status == "" {
		return "idle", "", 0, ""
	}

	switch wf.Status {
	case "publishing", "fetch_draft", "published", "md_writing", "md_written":
		return "publishing", wf.SessionID, wf.ChapterNumber, wf.VolumeName
	case "done":
		return "done", wf.SessionID, wf.ChapterNumber, wf.VolumeName
	case "done_partial":
		return "done_partial", wf.SessionID, wf.ChapterNumber, wf.VolumeName
	case "failed_gen", "failed_md", "published_failed":
		return "failed", wf.SessionID, wf.ChapterNumber, wf.VolumeName
	default:
		return "idle", wf.SessionID, wf.ChapterNumber, wf.VolumeName
	}
}
