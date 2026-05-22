package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/gin-gonic/gin"
)

const novelTitleSystemPrompt = `你是一个专业的网文书名创作专家。请根据用户提供的小说设定，生成3-5个候选书名。

要求：
- 分析小说分类、主题、角色设定、情节走向，提炼最核心的卖点和亮点
- 核心梗概前置：将故事最核心、最新颖的设定直接做进书名里，一眼吸睛
- 覆盖3种以上取名风格：悬念类、诗意类、直白爽点类
- 反差感与冲突感：通过身份、行为或环境的强烈反差制造吸引力
- 避免烂大街模板（总裁的XXX、废柴逆天、绝世无双、重生之XXX）
- 每个书名不超过15个字

输出格式（纯文本，不要 JSON，不要 Markdown）：
《书名一》—— 简短亮点说明
《书名二》—— 简短亮点说明
（依此类推）`

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int             `json:"max_tokens"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// NovelTitleSuggest 根据已选标签生成候选书名
func NovelTitleSuggest() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Topic string `json:"topic" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("topic 不能为空"))
			return
		}

		apiKey := os.Getenv("TEAM_DEEPSEEK_API_KEY")
		baseURL := os.Getenv("DEEPSEEK_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.deepseek.com/v1"
		}

		payload := openAIRequest{
			Model: "deepseek-chat",
			Messages: []openAIMessage{
				{Role: "system", Content: novelTitleSystemPrompt},
				{Role: "user", Content: req.Topic},
			},
			Temperature: 0.9,
			MaxTokens:   512,
		}

		bodyBytes, _ := json.Marshal(payload)
		httpReq, err := http.NewRequestWithContext(
			c.Request.Context(), http.MethodPost,
			baseURL+"/chat/completions",
			bytes.NewReader(bodyBytes),
		)
		if err != nil {
			model.Error(c, model.ErrInternal.WithDetail("构建请求失败"))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail("DeepSeek API 不可达: "+err.Error()))
			return
		}
		defer resp.Body.Close()

		respBytes, _ := io.ReadAll(resp.Body)
		var aiResp openAIResponse
		if err := json.Unmarshal(respBytes, &aiResp); err != nil {
			model.Error(c, model.ErrInternal.WithDetail("AI 响应解析失败"))
			return
		}
		if aiResp.Error != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(aiResp.Error.Message))
			return
		}
		if len(aiResp.Choices) == 0 {
			model.Error(c, model.ErrInternal.WithDetail("AI 未返回结果"))
			return
		}

		content := strings.TrimSpace(aiResp.Choices[0].Message.Content)

		// 按行拆分，每行为一个候选条目
		var titles []string
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				titles = append(titles, line)
			}
		}

		tid, _ := c.Get(model.TraceIDKey)
		c.JSON(http.StatusOK, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data:    gin.H{"titles": titles, "content": content},
			TraceID: tid.(string),
		})
	}
}
