package router

import (
	"fmt"
	"net/http"

	"github.com/claw-studio/L3_AI_BFF/config"
	"github.com/claw-studio/L3_AI_BFF/handler"
	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

func Setup(cfg *config.Config, wsProxy *proxy.WSProxy, autoPubMgr *handler.AutoPublishManager) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	r.Use(middleware.TraceID())
	r.Use(middleware.Logging())
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.AuthRequired())

	r.GET("/healthz", func(c *gin.Context) {
		tid, _ := c.Get(model.TraceIDKey)
		c.JSON(http.StatusOK, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data:    gin.H{"status": "healthy"},
			TraceID: tid.(string),
		})
	})

	api := r.Group("/api")
	{
		taskGroup := api.Group("/task")
		{
			taskGroup.POST("/create", handler.CreateTask(formatURL(cfg.SessionMgrURL, "")))
			taskGroup.GET("/list", handler.ListTask(formatURL(cfg.SessionMgrURL, "/api/task/list")))
			taskGroup.POST("/alloc_skill", handler.AllocSkill(cfg.SkillRegistryURL))
			taskGroup.POST("/stop", autoPubMgr.StopAutoPublish())
			taskGroup.POST("/finish", autoPubMgr.FinishAutoPublish())
			taskGroup.POST("/auto-publish/start", autoPubMgr.StartAutoPublish())
			taskGroup.GET("/:tid/timeline", handler.GetTaskTimeline(formatURL(cfg.SessionMgrURL, "/api/task/")))
			taskGroup.GET("/:tid/sessions", handler.TaskSessions(cfg.SessionMgrURL))
			taskGroup.GET("/:tid", handler.GetTask(cfg.SessionMgrURL))
			taskGroup.GET("/:tid/book/info", handler.BookGetInfo(cfg.SessionMgrURL))
			taskGroup.GET("/:tid/book/content", handler.BookGetContent(cfg.SessionMgrURL))
			taskGroup.GET("/:tid/publish/list", handler.GetTaskPublishList(cfg.C2DashboardURL))
			taskGroup.POST("/:tid/update", handler.TaskUpdate(cfg.SessionMgrURL))
			taskGroup.GET("/:tid/messages", handler.TaskMessages(cfg.SessionMgrURL))
			taskGroup.DELETE("/:tid", handler.DeleteTask(cfg.SessionMgrURL))
			taskGroup.POST("/:tid/publish", handler.PublishTask(formatURL(cfg.WorkflowURL, "/api/task"), cfg.SessionMgrURL, cfg.A1AccountURL))
		}

		sessionGroup := api.Group("/session")
		{
			sessionGroup.GET("/list", handler.ListSessions(formatURL(cfg.SessionMgrURL, "/api/sessions")))
			sessionGroup.POST("/create", handler.CreateSession(formatURL(cfg.SessionMgrURL, "/api/session/create")))
			sessionGroup.POST("/:sid/message", handler.SendMessage(formatURL(cfg.SessionMgrURL, "/api/session")))
			sessionGroup.POST("/:sid/close", handler.CloseSession(cfg.SessionMgrURL))
			sessionGroup.GET("/:sid/draft", handler.SessionDraft(formatURL(cfg.SessionMgrURL, "/api/session")))
		}

		api.GET("/dashboard/*path", handler.DashboardProxy(cfg.C2DashboardURL))

		api.GET("/account/*path", handler.AccountProxy(cfg.A1AccountURL))
		api.POST("/account/*path", handler.AccountProxyPost(cfg.A1AccountURL))

		api.GET("/skill/*path", handler.SkillProxy(cfg.SkillRegistryURL))

		api.GET("/models", handler.ModelProxy(cfg.AIModelURL))

		api.POST("/novel/title-suggest", handler.NovelTitleSuggest())

		api.GET("/publish/get_status", autoPubMgr.GetPublishStatus())
		api.GET("/publish/history", handler.GetPublishHistory(cfg.SessionMgrURL, cfg.WorkflowURL))
		api.GET("/publish/session", handler.GetPublishSession(cfg.SessionMgrURL, cfg.WorkflowURL))

		api.POST("/auth/login", handler.AuthLoginProxy(cfg.A1AccountURL))

		api.GET("/auth/me", handler.GetCurrentUser())
	}

	adminGroup := r.Group("/api/admin")
	adminGroup.Use(middleware.AdminRequired())
	{
		adminGroup.GET("/users", handler.AdminProxy(cfg.A1AccountURL))
		adminGroup.POST("/users", handler.AdminProxyPost(cfg.A1AccountURL))
		adminGroup.PUT("/users/*path", handler.AdminProxyPut(cfg.A1AccountURL))
		adminGroup.DELETE("/users/*path", handler.AdminProxyDelete(cfg.A1AccountURL))
	}

	auth := middleware.WSAuthRequired()

	r.GET("/ws/session/:session_id", func(c *gin.Context) {
		uid, err := auth(c.Writer, c.Request)
		if err != nil {
			return
		}

		sid := c.Param("session_id")
		upstreamURL := fmt.Sprintf("ws%s/api/session/%s/stream", wsScheme(cfg.SessionMgrURL), sid)
		if err := wsProxy.Proxy(c.Writer, c.Request, upstreamURL, uid); err != nil {
			return
		}
	})

	r.GET("/ws/chat/:task_id", func(c *gin.Context) {
		uid, err := auth(c.Writer, c.Request)
		if err != nil {
			return
		}

		tid := c.Param("task_id")
		upstreamURL := fmt.Sprintf("ws%s/api/task/%s/stream", wsScheme(cfg.SessionMgrURL), tid)
		if err := wsProxy.Proxy(c.Writer, c.Request, upstreamURL, uid); err != nil {
			return
		}
	})

	r.GET("/ws/task/:task_id", func(c *gin.Context) {
		uid, err := auth(c.Writer, c.Request)
		if err != nil {
			return
		}

		tid := c.Param("task_id")
		upstreamURL := fmt.Sprintf("ws%s/ws/task/%s", wsScheme(cfg.WorkflowURL), tid)
		if err := wsProxy.Proxy(c.Writer, c.Request, upstreamURL, uid); err != nil {
			return
		}
	})

	return r
}

func wsScheme(httpURL string) string {
	if len(httpURL) > 7 && httpURL[:7] == "http://" {
		return "://" + httpURL[7:]
	}
	if len(httpURL) > 8 && httpURL[:8] == "https://" {
		return "s://" + httpURL[8:]
	}
	return "://" + httpURL
}

func formatURL(base, path string) string {
	return fmt.Sprintf("%s%s", base, path)
}
