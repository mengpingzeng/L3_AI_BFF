package handler

import (
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

func AllocSkill(skillRegistryURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.AllocSkillReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		body := map[string]interface{}{
			"platform": req.Platform,
		}
		if req.Theme != "" {
			body["theme"] = req.Theme
		}
		if req.Style != "" {
			body["style"] = req.Style
		}

		respBody, statusCode, err := proxy.Forward(c, skillRegistryURL+"/api/skill/alloc", body)
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
