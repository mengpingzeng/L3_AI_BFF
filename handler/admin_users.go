package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

type adminUserListPayload struct {
	Users []adminUserRow `json:"users"`
	Total int            `json:"total"`
}

type adminUserRow struct {
	UID          string `json:"uid"`
	Username     string `json:"username"`
	Phone        string `json:"phone,omitempty"`
	Role         string `json:"role"`
	AccountCount int    `json:"accountCount"`
	TaskCount    int    `json:"taskCount"`
	CreatedAt    string `json:"createdAt"`
	LastLoginAt  string `json:"lastLoginAt,omitempty"`
}

type smTaskListPayload struct {
	Tasks []smTaskRow `json:"tasks"`
}

type smTaskRow struct {
	TaskID string `json:"task_id"`
	UID    string `json:"uid"`
}

// countSessionMgrTasksByUID 统计 Session Manager 中的创作任务数（与任务列表同源）。
// excludeStopped 为 true 时排除已停止自动发布的任务，与任务列表展示一致。
func countSessionMgrTasksByUID(sessionMgrURL string, excludeStopped func(string) bool) (map[string]int, error) {
	url := strings.TrimSuffix(sessionMgrURL, "/") + "/api/task/list?page=1&size=10000"
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("session_mgr list tasks: HTTP %d", resp.StatusCode)
	}

	var payload smTaskListPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	counts := make(map[string]int)
	for _, t := range payload.Tasks {
		if t.UID == "" {
			continue
		}
		if excludeStopped != nil && excludeStopped(t.TaskID) {
			continue
		}
		counts[t.UID]++
	}
	return counts, nil
}

// ListAdminUsers 拉取 A1 用户列表，并用 Session Manager 任务数覆盖 taskCount。
func ListAdminUsers(accountURL, sessionMgrURL string, autoPubMgr *AutoPublishManager) gin.HandlerFunc {
	var excludeStopped func(string) bool
	if autoPubMgr != nil {
		excludeStopped = autoPubMgr.IsStopped
	}

	return func(c *gin.Context) {
		url := accountURL + c.Request.URL.Path
		if rawQuery := c.Request.URL.RawQuery; rawQuery != "" {
			url += "?" + rawQuery
		}
		respBody, statusCode, err := proxy.ForwardGet(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		if statusCode < 200 || statusCode >= 300 {
			proxy.HandleDownstreamResponse(c, respBody, statusCode, "admin", func(c *gin.Context, data []byte) {
				c.Header("Content-Type", "application/json")
				c.String(statusCode, string(data))
			})
			return
		}

		var payload adminUserListPayload
		if err := json.Unmarshal(respBody, &payload); err != nil {
			proxy.HandleDownstreamResponse(c, respBody, statusCode, "admin", func(c *gin.Context, data []byte) {
				c.Header("Content-Type", "application/json")
				c.String(200, string(data))
			})
			return
		}

		if autoPubMgr != nil {
			autoPubMgr.ReloadStoppedTasks()
		}
		counts, err := countSessionMgrTasksByUID(sessionMgrURL, excludeStopped)
		if err == nil {
			for i := range payload.Users {
				payload.Users[i].TaskCount = counts[payload.Users[i].UID]
			}
		}

		out, err := json.Marshal(payload)
		if err != nil {
			model.Error(c, model.ErrInternal)
			return
		}
		c.Header("Content-Type", "application/json")
		c.String(http.StatusOK, string(out))
	}
}
