package handler

import (
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/pkg/idgen"
	"github.com/claw-studio/L3_AI_BFF/pkg/validator"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

func CreateTask(taskURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.CreateTaskReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		vr := validator.ValidateCreateTask(req.Topic, req.SkillID, req.Model, req.Platform)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		if len(req.AccountIDs) > 0 {
			avr := validator.ValidateAccountIDs(req.AccountIDs)
			if !avr.Valid {
				model.Error(c, model.ErrInvalidParam.WithDetail(avr.Errors))
				return
			}
		}

		tid, _ := c.Get(model.TraceIDKey)
		taskID := idgen.NewTaskID()
		uid, _ := c.Get("uid")

		model.Success(c, gin.H{
			"task_id":  taskID,
			"trace_id": tid,
			"uid":      uid,
		})
	}
}

func TaskUpdate(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		var body map[string]interface{}
		if err := c.ShouldBindJSON(&body); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}
		url := sessionMgrURL + "/api/task/" + tid + "/update"
		respBody, statusCode, err := proxy.Forward(c, url, body)
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

func DeleteTask(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		url := sessionMgrURL + "/api/task/" + tid
		respBody, statusCode, err := proxy.ForwardDelete(c, url)
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

func TaskSessions(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		url := sessionMgrURL + "/api/task/" + tid + "/sessions"
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

func ListTask(listURL string) gin.HandlerFunc {
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

		queryURL := listURL + "?page=" + intToStr(q.Page) + "&size=" + intToStr(q.Size)

		uid, _ := c.Get("uid")
		role, _ := c.Get("role")
		if role != "admin" && uid != nil {
			queryURL += "&uid=" + uid.(string)
		}

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

func GetTaskTimeline(timelineURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		var q model.TimelineQuery
		if err := c.ShouldBindQuery(&q); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("查询参数格式错误"))
			return
		}

		if q.Limit == 0 {
			q.Limit = 50
		}

		vr := validator.ValidateTimelineQuery(q.Cursor, q.Limit)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		uid, _ := c.Get("uid")
		role, _ := c.Get("role")

		queryURL := timelineURL + tid + "/timeline?limit=" + intToStr(q.Limit)
		if q.Cursor != "" {
			queryURL += "&cursor=" + q.Cursor
		}
		if role != "admin" && uid != nil {
			queryURL += "&uid=" + uid.(string)
		}

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
