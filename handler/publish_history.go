package handler

import (
	"encoding/json"
	"sort"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

type publishHistoryItem struct {
	SessionID     string `json:"session_id"`
	ChapterNumber int    `json:"chapter_number"`
	VolumeName    string `json:"volume_name,omitempty"`
	CreatedAt     string `json:"created_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
}

type publishHistoryResp struct {
	TaskID    string               `json:"task_id"`
	Histories []publishHistoryItem `json:"histories"`
}

func GetPublishHistory(sessionMgrURL, workflowURL, dashboardURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)
		taskID := c.Query("task_id")

		if taskID == "" {
			if logger != nil {
				logger.Warn(logging.ErrInvalidParam, "publish/history: task_id is empty")
			}
			model.Error(c, model.ErrInvalidParam.WithDetail("task_id is required"))
			return
		}

		if logger != nil {
			logger.Info("publish/history: start fetch task=%s", taskID)
		}

		// 1. 从 Dashboard 的 publish_record 获取真正发布过的 session_id 集合
		dashBody := map[string]interface{}{
			"taskId": taskID,
			"size":   500,
		}
		dashBodyBytes, dashStatus, err := proxy.Forward(c, dashboardURL+"/api/dashboard/query", dashBody)
		if err != nil || dashStatus >= 400 {
			if logger != nil {
				logger.Error(logging.ErrDatabaseError,
					"publish/history: dashboard query failed task=%s status=%d err=%v body=%s",
					taskID, dashStatus, err, truncate(dashBodyBytes, 300))
			}
			model.Error(c, model.ErrInternal)
			return
		}

		var dashResp struct {
			Items []struct {
				SessionID   string `json:"sessionId"`
				PublishedAt string `json:"publishedAt"`
			} `json:"items"`
		}
		if err := json.Unmarshal(dashBodyBytes, &dashResp); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError,
					"publish/history: parse dashboard failed task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		// 按 session_id 去重 + 取最新的 published_at
		publishedSessions := make(map[string]string)
		for _, item := range dashResp.Items {
			if item.SessionID == "" {
				continue
			}
			if existing, ok := publishedSessions[item.SessionID]; !ok || item.PublishedAt > existing {
				publishedSessions[item.SessionID] = item.PublishedAt
			}
		}

		// 2. 获取所有 Session（用于匹配 chapter_number / volume_name / created_at）
		sessionsBody, sessionsStatus, err := proxy.ForwardGet(c, sessionMgrURL+"/api/task/"+taskID+"/sessions")
		if err != nil || sessionsStatus >= 400 {
			if logger != nil {
				logger.Error(logging.ErrDatabaseError,
					"publish/history: get sessions failed task=%s status=%d err=%v body=%s",
					taskID, sessionsStatus, err, truncate(sessionsBody, 300))
			}
			model.Error(c, model.ErrInternal)
			return
		}

		var sessionsResp struct {
			Sessions []publishSessionRaw `json:"sessions"`
		}
		if err := json.Unmarshal(sessionsBody, &sessionsResp); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError,
					"publish/history: parse sessions failed task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		sessionMap := make(map[string]publishSessionRaw)
		for _, s := range sessionsResp.Sessions {
			sessionMap[s.SessionID] = s
		}

		// 3. 组装：对每个发布过的 session_id，从 SM 匹配时间信息
		histories := make([]publishHistoryItem, 0, len(publishedSessions))
		for sid, publishedAt := range publishedSessions {
			item := publishHistoryItem{
				SessionID:  sid,
				FinishedAt: publishedAt,
			}
			if sm, ok := sessionMap[sid]; ok {
				item.ChapterNumber = sm.ChapterNumber
				item.VolumeName = sm.VolumeName
				item.CreatedAt = sm.CreatedAt
			}
			histories = append(histories, item)
		}

		sort.Slice(histories, func(i, j int) bool {
			return histories[i].ChapterNumber > histories[j].ChapterNumber
		})

		if logger != nil {
			logger.Info("publish/history: done task=%s published_sessions=%d matched_sm=%d final=%d",
				taskID, len(publishedSessions), len(sessionMap), len(histories))
		}

		model.Success(c, publishHistoryResp{
			TaskID:    taskID,
			Histories: histories,
		})
	}
}

type publishSessionRaw struct {
	SessionID     string `json:"session_id"`
	ChapterNumber int    `json:"chapter_number"`
	VolumeName    string `json:"volume_name"`
	CreatedAt     string `json:"created_at"`
	ArchivedAt    string `json:"archived_at,omitempty"`
}

func truncate(data []byte, maxLen int) string {
	s := string(data)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
