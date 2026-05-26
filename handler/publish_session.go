package handler

import (
	"encoding/json"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

type publishSessionResp struct {
	TaskID         string                 `json:"task_id"`
	PlatformSessID string                 `json:"platform_session_id"`
	ChapterNumber  int                    `json:"chapter_number"`
	VolumeName     string                 `json:"volume_name,omitempty"`
	Status         string                 `json:"status"`
	CreatedAt      string                 `json:"created_at"`
	FinishedAt     string                 `json:"finished_at,omitempty"`
	PublishResults []publishSessionResult `json:"publish_results,omitempty"`
	Accounts       []publishSessionAcct   `json:"accounts,omitempty"`
	Source         string                 `json:"source"`
}

type publishSessionResult struct {
	AccountID     string `json:"accountId"`
	Platform      string `json:"platform"`
	Status        string `json:"status"`
	PostID        string `json:"postId,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
	MaskedDisplay string `json:"maskedDisplay,omitempty"`
}

type publishSessionAcct struct {
	AccountID string `json:"accountId"`
	UID       string `json:"uid"`
	Platform  string `json:"platform"`
}

func GetPublishSession(sessionMgrURL, workflowURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)
		taskID := c.Query("task_id")
		platformSessID := c.Query("platform_session_id")

		if taskID == "" {
			if logger != nil {
				logger.Warn(logging.ErrInvalidParam, "publish/session: task_id is empty")
			}
			model.Error(c, model.ErrInvalidParam.WithDetail("task_id is required"))
			return
		}
		if platformSessID == "" {
			if logger != nil {
				logger.Warn(logging.ErrInvalidParam, "publish/session: platform_session_id is empty")
			}
			model.Error(c, model.ErrInvalidParam.WithDetail("platform_session_id is required"))
			return
		}

		if logger != nil {
			logger.Info("publish/session: start task=%s platform_sess=%s", taskID, platformSessID)
		}

		wfURL := workflowURL + "/api/task/" + taskID + "/status"
		wfBody, wfStatus, wfErr := proxy.ForwardGet(c, wfURL)

		if wfErr != nil || wfStatus >= 400 {
			if logger != nil {
				logger.Error(logging.ErrExternalService,
					"publish/session: workflow status failed task=%s url=%s status=%d err=%v body=%s",
					taskID, wfURL, wfStatus, wfErr, truncate(wfBody, 500))
			}
			model.Error(c, model.ErrInternal)
			return
		}

		var wfTask struct {
			TaskID         string                 `json:"task_id"`
			Status         string                 `json:"status"`
			SessionID      string                 `json:"session_id"`
			VolumeName     string                 `json:"volume_name"`
			ChapterNumber  int                    `json:"chapter_number"`
			ErrorMsg       string                 `json:"error_msg"`
			PublishResults []publishSessionResult `json:"publish_results"`
			Accounts       []publishSessionAcct   `json:"accounts"`
			CreatedAt      string                 `json:"created_at"`
			UpdatedAt      string                 `json:"updated_at"`
			Exists         bool                   `json:"exists"`
		}
		if err := json.Unmarshal(wfBody, &wfTask); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError,
					"publish/session: parse workflow status failed task=%s err=%v raw=%s",
					taskID, err, truncate(wfBody, 500))
			}
			model.Error(c, model.ErrInternal)
			return
		}

		if !wfTask.Exists {
			if logger != nil {
				logger.Info("publish/session: no workflow task exists for task=%s, fallback to session_mgr", taskID)
			}
			resp := buildHistResp(logger, sessionMgrURL, taskID, platformSessID)
			model.Success(c, resp)
			return
		}

		// workflow_task.session_id 匹配 → 当前/最近的发布会话
		if wfTask.SessionID == platformSessID {
			if logger != nil {
				logger.Info("publish/session: matched active workflow task=%s session=%s status=%s",
					taskID, platformSessID, wfTask.Status)
			}

			finishedAt := wfTask.UpdatedAt
			if isTerminalPub(wfTask.Status) && finishedAt == "" {
				finishedAt = wfTask.CreatedAt
			}

			uid, _ := c.Get("uid")
			if logger != nil {
				total := len(wfTask.PublishResults)
				okCount := countOK(wfTask.PublishResults)
				logger.Info("publish/session: done task=%s session=%s status=%s platforms=%d ok=%d fail=%d source=workflow uid=%v",
					taskID, platformSessID, wfTask.Status, total, okCount, total-okCount, uid)
			}

			model.Success(c, publishSessionResp{
				TaskID:         taskID,
				PlatformSessID: platformSessID,
				ChapterNumber:  wfTask.ChapterNumber,
				VolumeName:     wfTask.VolumeName,
				Status:         wfTask.Status,
				CreatedAt:      wfTask.CreatedAt,
				FinishedAt:     finishedAt,
				PublishResults: wfTask.PublishResults,
				Accounts:       wfTask.Accounts,
				Source:         "workflow",
			})
			return
		}

		// 不匹配 → 历史发布会话, 从 Session Mgr 补数据
		if logger != nil {
			logger.Info("publish/session: session mismatch, fallback to session_mgr: task=%s platform_sess=%s wf_sess=%s",
				taskID, platformSessID, wfTask.SessionID)
		}

		resp := buildHistResp(logger, sessionMgrURL, taskID, platformSessID)
		model.Success(c, resp)
	}
}

func buildHistResp(logger *logging.Logger, sessionMgrURL, taskID, platformSessID string) publishSessionResp {
	resp := publishSessionResp{
		TaskID:         taskID,
		PlatformSessID: platformSessID,
		Status:         "published",
		Source:         "session_mgr",
	}

	sessionsBody, err := doDownstreamGet(sessionMgrURL + "/api/task/" + taskID + "/sessions")
	if err != nil {
		if logger != nil {
			logger.Warn(logging.WarnServiceDegraded,
				"publish/session: get sessions for history failed task=%s err=%v", taskID, err)
		}
		return resp
	}

	var sessionsResp struct {
		Sessions []publishSessionRaw `json:"sessions"`
	}
	if err := json.Unmarshal(sessionsBody, &sessionsResp); err != nil {
		if logger != nil {
			logger.Warn(logging.WarnServiceDegraded,
				"publish/session: parse sessions for history failed task=%s err=%v", taskID, err)
		}
		return resp
	}

	for _, s := range sessionsResp.Sessions {
		if s.SessionID == platformSessID {
			resp.ChapterNumber = s.ChapterNumber
			resp.VolumeName = s.VolumeName
			resp.CreatedAt = s.CreatedAt
			resp.FinishedAt = s.ArchivedAt
			if logger != nil {
				logger.Info("publish/session: historical session task=%s session=%s chapter=%d source=session_mgr",
					taskID, platformSessID, s.ChapterNumber)
			}
			break
		}
	}

	return resp
}

func isTerminalPub(status string) bool {
	switch status {
	case "done", "done_partial", "published_failed", "failed_gen", "failed_md":
		return true
	}
	return false
}

func countOK(results []publishSessionResult) int {
	n := 0
	for _, r := range results {
		if r.Status == "ok" {
			n++
		}
	}
	return n
}
