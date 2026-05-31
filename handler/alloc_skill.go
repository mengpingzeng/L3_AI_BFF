package handler

import (
	"encoding/json"
	"net/http"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

var httpClient = &http.Client{}

func fetchActiveSkillIDs(sessionMgrURL string) []string {
	resp, err := httpClient.Get(sessionMgrURL + "/api/task/skill-ids")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var result struct {
		SkillIDs []string `json:"skill_ids"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return nil
	}
	return result.SkillIDs
}

func AllocSkill(skillRegistryURL, sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.AllocSkillReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		excludeIDs := fetchActiveSkillIDs(sessionMgrURL)

		body := map[string]interface{}{
			"platform": req.Platform,
		}
		if req.Theme != "" {
			body["theme"] = req.Theme
		}
		if req.Style != "" {
			body["style"] = req.Style
		}
		if len(excludeIDs) > 0 {
			body["exclude_ids"] = excludeIDs
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

func ReleaseSkill(skillRegistryURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			SkillID string `json:"skill_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}
		if req.SkillID == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("skill_id 不能为空"))
			return
		}

		body := map[string]interface{}{
			"skill_id": req.SkillID,
		}
		respBody, statusCode, err := proxy.Forward(c, skillRegistryURL+"/api/skill/alloc/release", body)
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

func AvailableSkillCount(skillRegistryURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		queryURL := skillRegistryURL + "/api/skill/alloc/available"
		if q := c.Request.URL.RawQuery; q != "" {
			queryURL += "?" + q
		}

		respBody, statusCode, err := proxy.ForwardGet(c, queryURL)
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
