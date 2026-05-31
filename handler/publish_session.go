package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

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

func GetPublishSession(sessionMgrURL, workflowURL, dashboardURL string) gin.HandlerFunc {
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

		// 1. 先查 WF：如果当前/最近 WF 任务的 session_id 匹配，直接返回 WF 数据
		wfURL := workflowURL + "/api/task/" + taskID + "/status"
		wfBody, wfStatus, wfErr := proxy.ForwardGet(c, wfURL)

		if wfErr == nil && wfStatus < 400 {
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
			if json.Unmarshal(wfBody, &wfTask) == nil && wfTask.Exists && wfTask.SessionID == platformSessID {
				finishedAt := wfTask.UpdatedAt
				if isTerminalPub(wfTask.Status) && finishedAt == "" {
					finishedAt = wfTask.CreatedAt
				}
				if logger != nil {
					total := len(wfTask.PublishResults)
					okCount := countOK(wfTask.PublishResults)
					logger.Info("publish/session: matched workflow task=%s session=%s status=%s platforms=%d ok=%d fail=%d source=workflow",
						taskID, platformSessID, wfTask.Status, total, okCount, total-okCount)
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
		}

		// 2. WF 不匹配或不可用 → 回退到 publish_record 查历史
		if logger != nil {
			logger.Info("publish/session: fallback to publish_record task=%s session=%s", taskID, platformSessID)
		}

		resp := buildDashboardFallbackResp(logger, sessionMgrURL, dashboardURL, taskID, platformSessID)
		model.Success(c, resp)
	}
}

func buildDashboardFallbackResp(logger *logging.Logger, sessionMgrURL, dashboardURL, taskID, platformSessID string) publishSessionResp {
	resp := publishSessionResp{
		TaskID:         taskID,
		PlatformSessID: platformSessID,
		Status:         "published",
		Source:         "publish_record",
	}

	// 查 Dashboard 获取发布到各平台/账号的记录
	type dashItem struct {
		AccountID   string `json:"accountId"`
		Platform    string `json:"platform"`
		PostID      string `json:"postId"`
		LoginName   string `json:"loginName"`
		PublishedAt string `json:"publishedAt"`
	}

	dashBody := map[string]interface{}{
		"taskId":     taskID,
		"sessionIds": []string{platformSessID},
		"size":       200,
	}
	dashBytes, dashStatus, dashErr := postJSON(dashboardURL+"/api/dashboard/query", dashBody)

	if dashErr == nil && dashStatus < 400 {
		var dashResp struct {
			Items []dashItem `json:"items"`
		}
		if json.Unmarshal(dashBytes, &dashResp) == nil && len(dashResp.Items) > 0 {
			seen := make(map[string]bool)
			for _, item := range dashResp.Items {
				resp.PublishResults = append(resp.PublishResults, publishSessionResult{
					AccountID:     item.AccountID,
					Platform:      item.Platform,
					Status:        "ok",
					PostID:        item.PostID,
					MaskedDisplay: item.LoginName,
				})
				if !seen[item.AccountID] {
					seen[item.AccountID] = true
					resp.Accounts = append(resp.Accounts, publishSessionAcct{
						AccountID: item.AccountID,
						Platform:  item.Platform,
					})
				}
				if resp.FinishedAt == "" || item.PublishedAt > resp.FinishedAt {
					resp.FinishedAt = item.PublishedAt
				}
			}
			resp.Status = "done"
			if logger != nil {
				logger.Info("publish/session: found in publish_record task=%s session=%s results=%d",
					taskID, platformSessID, len(resp.PublishResults))
			}
		} else if logger != nil {
			logger.Warn(logging.WarnServiceDegraded,
				"publish/session: session not found in publish_record task=%s session=%s", taskID, platformSessID)
		}
	} else if logger != nil {
		logger.Warn(logging.WarnServiceDegraded,
			"publish/session: dashboard query failed task=%s session=%s err=%v", taskID, platformSessID, dashErr)
	}

	// 从 SM 补 session 时间信息
	sessionsBody, err := doDownstreamGet(sessionMgrURL + "/api/task/" + taskID + "/sessions")
	if err == nil {
		var sessionsResp struct {
			Sessions []publishSessionRaw `json:"sessions"`
		}
		if json.Unmarshal(sessionsBody, &sessionsResp) == nil {
			for _, s := range sessionsResp.Sessions {
				if s.SessionID == platformSessID {
					resp.ChapterNumber = s.ChapterNumber
					resp.VolumeName = s.VolumeName
					if resp.CreatedAt == "" {
						resp.CreatedAt = s.CreatedAt
					}
					if resp.FinishedAt == "" {
						resp.FinishedAt = s.ArchivedAt
					}
					break
				}
			}
		}
	}

	// 确实没找到任何发布记录 → 未发布
	if len(resp.PublishResults) == 0 {
		resp.Status = "unpublished"
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

func postJSON(url string, body map[string]interface{}) ([]byte, int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return respBody, resp.StatusCode, nil
}
