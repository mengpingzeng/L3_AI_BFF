package handler

import (
	"fmt"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/pkg/validator"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

func CreateSession(sessionURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.CreateSessionReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		vr := validator.ValidateCreateSession(req.TaskID)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		tid, _ := c.Get(model.TraceIDKey)
		uid, _ := c.Get("uid")

		downstream := map[string]interface{}{
			"task_id":    req.TaskID,
			"skill_id":   req.SkillID,
			"skillVer":   req.SkillVer,
			"model":      req.Model,
			"topic":      req.Topic,
			"platform":   req.Platform,
			"account_id": req.AccountID,
			"novel_name": req.NovelName,
			"uid":        uid,
			"trace_id":   tid,
		}

		respBody, statusCode, err := proxy.Forward(c, sessionURL, downstream)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func ListSessions(listURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var q model.TaskListQuery
		if err := c.ShouldBindQuery(&q); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("查询参数格式错误"))
			return
		}

		if q.Page == 0 {
			q.Page = 1
		}
		if q.Size == 0 {
			q.Size = 20
		}

		vr := validator.ValidatePagination(q.Page, q.Size)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		tid, _ := c.Get(model.TraceIDKey)
		uid, _ := c.Get("uid")

		queryURL := fmt.Sprintf("%s?page=%s&size=%s&uid=%v&trace_id=%v",
			listURL, intToStr(q.Page), intToStr(q.Size), uid, tid)

		respBody, statusCode, err := proxy.ForwardGet(c, queryURL)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func SendMessage(messageURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		sid := c.Param("sid")
		if !validator.IsValidSessionID(sid) {
			model.Error(c, model.ErrInvalidParam.WithDetail("会话 ID 格式不合法"))
			return
		}

		var req model.SendMessageReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		vr := validator.ValidateSendMessage(req.Text)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		tid, _ := c.Get(model.TraceIDKey)
		uid, _ := c.Get("uid")

		downstream := map[string]interface{}{
			"text":          req.Text,
			"draft_version": req.DraftVersion,
			"uid":           uid,
			"trace_id":      tid,
		}

		url := formatURL(messageURL, "/"+sid+"/send")
		respBody, statusCode, err := proxy.Forward(c, url, downstream)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func SessionDraft(draftURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		sid := c.Param("sid")

		uid, _ := c.Get("uid")
		role, _ := c.Get("role")

		url := formatURL(draftURL, "/"+sid+"/draft")
		if role != "admin" && uid != nil {
			url += "?uid=" + uid.(string)
		}

		respBody, statusCode, err := proxy.ForwardGet(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}
