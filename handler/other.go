package handler

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

var dashboardArrayKeys = map[string]bool{
	"platforms": true, "accountIds": true, "skillIds": true, "sessionIds": true,
}

func DashboardProxy(dashboardURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		body := make(map[string]interface{})
		query := c.Request.URL.Query()
		for key, values := range query {
			if dashboardArrayKeys[key] {
				body[key] = values
			} else if len(values) == 1 {
				body[key] = values[0]
			} else {
				body[key] = values
			}
		}

		uid, _ := c.Get("uid")
		role, _ := c.Get("role")
		if role != "admin" && uid != nil {
			body["uid"] = uid
		}

		respBody, statusCode, err := proxy.Forward(c, dashboardURL+c.Request.URL.Path, body)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "dashboard", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func AccountProxy(accountURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		url := accountURL + c.Request.URL.Path

		rawQuery := c.Request.URL.RawQuery
		uid, _ := c.Get("uid")
		role, _ := c.Get("role")
		if role != "admin" && uid != nil {
			if rawQuery != "" {
				url += "?" + rawQuery + "&uid=" + uid.(string)
			} else {
				url += "?uid=" + uid.(string)
			}
			c.Request.URL.RawQuery = ""
		}

		respBody, statusCode, err := proxy.ForwardGet(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "account", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func AccountProxyPost(accountURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body map[string]interface{}
		if err := c.ShouldBindJSON(&body); err != nil {
			body = nil
		}

		if body != nil {
			uid, _ := c.Get("uid")
			if uid != nil {
				body["uid"] = uid
			}
			role, _ := c.Get("role")
			if role != nil {
				body["role"] = role
			}
		}

		respBody, statusCode, err := proxy.Forward(c, accountURL+c.Request.URL.Path, body)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "account", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func SkillProxy(skillURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		respBody, statusCode, err := proxy.ForwardGet(c, skillURL+c.Request.URL.Path)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "skill_registry", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func ModelProxy(modelURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		respBody, statusCode, err := proxy.ForwardGet(c, modelURL+"/api/models")
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "ai_model", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func AuthLoginProxy(accountURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body map[string]interface{}
		if err := c.ShouldBindJSON(&body); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		respBody, statusCode, err := proxy.Forward(c, accountURL+"/api/auth/login", body)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "auth", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func GetCurrentUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get("uid")
		role, _ := c.Get("role")
		username, _ := c.Get("username")
		tid, _ := c.Get(model.TraceIDKey)
		c.JSON(http.StatusOK, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data: gin.H{
				"uid":      uid,
				"username": username,
				"role":     role,
			},
			TraceID: tid.(string),
		})
	}
}

func AdminProxy(accountURL string) gin.HandlerFunc {
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
		proxy.HandleDownstreamResponse(c, respBody, statusCode, "admin", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func AdminProxyPost(accountURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body map[string]interface{}
		if err := c.ShouldBindJSON(&body); err != nil {
			body = nil
		}
		respBody, statusCode, err := proxy.Forward(c, accountURL+c.Request.URL.Path, body)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		proxy.HandleDownstreamResponse(c, respBody, statusCode, "admin", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func AdminProxyPut(accountURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("读取请求体失败"))
			return
		}

		path := c.Param("path")
		url := fmt.Sprintf("%s/api/admin/users%s", strings.TrimSuffix(accountURL, "/"), path)

		tid, _ := c.Get(model.TraceIDKey)
		uid, _ := c.Get("uid")

		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPut, url, strings.NewReader(string(bodyBytes)))
		if err != nil {
			model.Error(c, model.ErrInternal)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if tid != nil {
			req.Header.Set("X-Trace-ID", tid.(string))
		}
		if uid != nil {
			req.Header.Set("X-User-ID", uid.(string))
		}
		if role, ok := c.Get("role"); ok {
			req.Header.Set("X-User-Role", role.(string))
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		proxy.HandleDownstreamResponse(c, respBody, resp.StatusCode, "admin", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func AdminProxyDelete(accountURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Param("path")
		url := fmt.Sprintf("%s/api/admin/users%s", strings.TrimSuffix(accountURL, "/"), path)

		tid, _ := c.Get(model.TraceIDKey)
		uid, _ := c.Get("uid")

		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodDelete, url, nil)
		if err != nil {
			model.Error(c, model.ErrInternal)
			return
		}
		if tid != nil {
			req.Header.Set("X-Trace-ID", tid.(string))
		}
		if uid != nil {
			req.Header.Set("X-User-ID", uid.(string))
		}
		if role, ok := c.Get("role"); ok {
			req.Header.Set("X-User-Role", role.(string))
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		proxy.HandleDownstreamResponse(c, respBody, resp.StatusCode, "admin", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}
