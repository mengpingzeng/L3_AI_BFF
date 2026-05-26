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

func GetPublishHistory(sessionMgrURL, workflowURL string) gin.HandlerFunc {
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

		// 1. 获取 Task 信息
		taskURL := sessionMgrURL + "/api/task/" + taskID
		taskBody, taskStatus, err := proxy.ForwardGet(c, taskURL)
		if err != nil || taskStatus >= 400 {
			if logger != nil {
				logger.Error(logging.ErrNotFound,
					"publish/history: get task upstream failed task=%s url=%s status=%d err=%v body=%s",
					taskID, taskURL, taskStatus, err, truncate(taskBody, 500))
			}
			model.Error(c, model.ErrNotFound.WithDetail("任务不存在"))
			return
		}

		var task struct {
			VolumeName            string `json:"volume_name"`
			PublishedChapterCount int    `json:"published_chapter_count"`
			ActiveSessionID       string `json:"active_session_id"`
		}
		if err := json.Unmarshal(taskBody, &task); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError,
					"publish/history: parse task failed task=%s err=%v raw=%s",
					taskID, err, truncate(taskBody, 500))
			}
			model.Error(c, model.ErrInternal)
			return
		}

		if logger != nil {
			logger.Info("publish/history: task loaded task=%s published_chapter_count=%d active_session_id=%s",
				taskID, task.PublishedChapterCount, task.ActiveSessionID)
		}

		// 2. 获取所有 Session
		sessionsURL := sessionMgrURL + "/api/task/" + taskID + "/sessions"
		sessionsBody, sessionsStatus, err := proxy.ForwardGet(c, sessionsURL)
		if err != nil || sessionsStatus >= 400 {
			if logger != nil {
				logger.Error(logging.ErrDatabaseError,
					"publish/history: get sessions upstream failed task=%s url=%s status=%d err=%v body=%s",
					taskID, sessionsURL, sessionsStatus, err, truncate(sessionsBody, 500))
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
					"publish/history: parse sessions failed task=%s err=%v raw=%s",
					taskID, err, truncate(sessionsBody, 500))
			}
			model.Error(c, model.ErrInternal)
			return
		}

		totalSessions := len(sessionsResp.Sessions)

		// 3. 获取 Workflow 发布状态（可选，用于补充活跃发布会话）
		activeSid := task.ActiveSessionID
		wfURL := workflowURL + "/api/task/" + taskID + "/status"
		wfBody, wfStatus, wfErr := proxy.ForwardGet(c, wfURL)
		if wfErr != nil || wfStatus >= 400 {
			if logger != nil {
				logger.Warn(logging.WarnServiceDegraded,
					"publish/history: workflow status unavailable task=%s url=%s status=%d err=%v body=%s",
					taskID, wfURL, wfStatus, wfErr, truncate(wfBody, 300))
			}
		} else {
			var wf struct {
				SessionID string `json:"session_id"`
				Exists    bool   `json:"exists"`
			}
			if err := json.Unmarshal(wfBody, &wf); err != nil {
				if logger != nil {
					logger.Warn(logging.WarnServiceDegraded,
						"publish/history: workflow status parse failed task=%s err=%v raw=%s",
						taskID, err, truncate(wfBody, 300))
				}
			} else if wf.Exists && wf.SessionID != "" {
				activeSid = wf.SessionID
				if logger != nil {
					logger.Info("publish/history: workflow active session task=%s session=%s", taskID, activeSid)
				}
			}
		}

		// 4. 按 PublishedChapterCount 过滤已发布的 session
		publishedSet := make(map[string]bool)
		matchedByChapter := 0
		for _, s := range sessionsResp.Sessions {
			if s.ChapterNumber > 0 && s.ChapterNumber <= task.PublishedChapterCount {
				publishedSet[s.SessionID] = true
				matchedByChapter++
			}
		}
		if activeSid != "" {
			if !publishedSet[activeSid] {
				publishedSet[activeSid] = true
				if logger != nil {
					logger.Info("publish/history: added active session=%s not yet in published range", activeSid)
				}
			}
		}

		// 5. 组装结果
		histories := make([]publishHistoryItem, 0, len(publishedSet))
		var skippedZero, skippedNotPublished int
		for _, s := range sessionsResp.Sessions {
			if s.ChapterNumber == 0 {
				skippedZero++
				continue
			}
			if !publishedSet[s.SessionID] {
				skippedNotPublished++
				continue
			}
			histories = append(histories, publishHistoryItem{
				SessionID:     s.SessionID,
				ChapterNumber: s.ChapterNumber,
				VolumeName:    s.VolumeName,
				CreatedAt:     s.CreatedAt,
				FinishedAt:    s.ArchivedAt,
			})
		}

		sort.Slice(histories, func(i, j int) bool {
			return histories[i].ChapterNumber > histories[j].ChapterNumber
		})

		uid, _ := c.Get("uid")
		if logger != nil {
			logger.Info("publish/history: done task=%s total_sessions=%d filtered=%d (chapter_limit=%d, matched=%d, active_added=%v) skipped_zero=%d skipped_not_published=%d final=%d uid=%v",
				taskID, totalSessions, len(histories),
				task.PublishedChapterCount, matchedByChapter, activeSid != "" && activeSid != task.ActiveSessionID,
				skippedZero, skippedNotPublished, len(histories), uid)
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
