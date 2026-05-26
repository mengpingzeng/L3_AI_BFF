package handler

import (
	"encoding/json"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

type publishStatusData struct {
	LatestVolumeName       string `json:"latest_volume_name"`
	LatestChapterNumber    int    `json:"latest_chapter_number"`
	ActiveSessionID        string `json:"active_session_id"`
	PublishStatus          string `json:"publish_status"`
	IsAutoPublishRunning   bool   `json:"is_auto_publish_running"`
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
			VolumeName            string `json:"volume_name"`
			PublishedChapterCount int    `json:"published_chapter_count"`
			ActiveSessionID       string `json:"active_session_id"`
		}
		if err := json.Unmarshal(taskData, &task); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError, "publish/get_status: parse task failed: task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		publishStatus := mapWorkflowStatus(taskID, m)
		if logger != nil {
			logger.Info("publish/get_status: workflow status: task=%s status=%s", taskID, publishStatus)
		}

		isRunning := m.isAutoPublishRunning(taskID)

		data := publishStatusData{
			LatestVolumeName:       task.VolumeName,
			LatestChapterNumber:    task.PublishedChapterCount,
			ActiveSessionID:        task.ActiveSessionID,
			PublishStatus:          publishStatus,
			IsAutoPublishRunning:   isRunning,
		}

		if logger != nil {
			logger.Info("publish/get_status: response task=%s volume=%s chapter=%d status=%s active_session=%s auto_running=%v",
				taskID, data.LatestVolumeName, data.LatestChapterNumber, data.PublishStatus, data.ActiveSessionID, data.IsAutoPublishRunning)
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

func mapWorkflowStatus(taskID string, m *AutoPublishManager) string {
	respBody, err := m.doGet(m.workflowURL + "/api/task/" + taskID + "/status")
	if err != nil {
		return "idle"
	}

	var wfStatus struct {
		Status string `json:"status"`
		Exists bool   `json:"exists"`
	}
	if err := json.Unmarshal(respBody, &wfStatus); err != nil {
		return "idle"
	}
	if !wfStatus.Exists || wfStatus.Status == "" {
		return "idle"
	}

	switch wfStatus.Status {
	case "publishing":
		return "publishing"
	case "failed_gen", "failed_md", "published_failed":
		return "failed"
	default:
		return "idle"
	}
}
