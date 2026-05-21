package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/gin-gonic/gin"
)

var httpClient = &http.Client{
	Timeout: 210 * time.Second,
}

func Forward(c *gin.Context, upstreamURL string, bodyData map[string]interface{}) ([]byte, int, error) {
	tid, _ := c.Get(model.TraceIDKey)

	jsonBody, err := jsonMarshal(bodyData)
	if err != nil {
		return nil, 0, err
	}

	log.Printf("[转发] POST %s | trace_id=%v | body=%s", upstreamURL, tid, truncate(string(jsonBody), 300))

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	if tid != nil {
		req.Header.Set("X-Trace-ID", tid.(string))
	}
	if uid, ok := c.Get("uid"); ok {
		req.Header.Set("X-User-ID", uid.(string))
	}
	if role, ok := c.Get("role"); ok {
		req.Header.Set("X-User-Role", role.(string))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[转发失败] POST %s | trace_id=%v | err=%v", upstreamURL, tid, err)
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	log.Printf("[转发完成] POST %s | trace_id=%v | 下游状态=%d | 返回长度=%d", upstreamURL, tid, resp.StatusCode, len(respBody))
	return respBody, resp.StatusCode, nil
}

func ForwardGet(c *gin.Context, upstreamURL string) ([]byte, int, error) {
	tid, _ := c.Get(model.TraceIDKey)

	log.Printf("[转发] GET %s | trace_id=%v", upstreamURL, tid)

	if c.Request.URL.RawQuery != "" {
		upstreamURL += "?" + c.Request.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		return nil, 0, err
	}

	if tid != nil {
		req.Header.Set("X-Trace-ID", tid.(string))
	}
	if uid, ok := c.Get("uid"); ok {
		req.Header.Set("X-User-ID", uid.(string))
	}
	if role, ok := c.Get("role"); ok {
		req.Header.Set("X-User-Role", role.(string))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[转发失败] GET %s | trace_id=%v | err=%v", upstreamURL, tid, err)
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	log.Printf("[转发完成] GET %s | trace_id=%v | 下游状态=%d | 返回长度=%d", upstreamURL, tid, resp.StatusCode, len(respBody))
	return respBody, resp.StatusCode, nil
}

func MapDownstreamError(service string, downstreamStatus int, downstreamBody []byte) *model.AppError {
	switch downstreamStatus {
	case 400:
		return model.ErrInvalidParam
	case 401, 403:
		return model.ErrUnauthorized
	case 404:
		return model.ErrNotFound
	case 429:
		return model.ErrRateLimited
	case 500:
		return model.ErrInternal
	case 502, 503:
		return model.ErrUpstreamUnavailable
	case 504:
		return model.ErrUpstreamTimeout
	default:
		if downstreamStatus >= 500 {
			return model.ErrUpstreamUnavailable
		}
		return model.ErrInternal
	}
}

func HandleDownstreamResponse(c *gin.Context, respBody []byte, statusCode int, service string, successHandler func(c *gin.Context, data []byte)) {
	if statusCode >= 200 && statusCode < 300 {
		successHandler(c, respBody)
		return
	}

	appErr := MapDownstreamError(service, statusCode, respBody)
	tid, _ := c.Get(model.TraceIDKey)
	if tid != nil {
		appErr.TraceID = tid.(string)
	}

	detail := string(respBody)
	if len(detail) > 500 {
		detail = detail[:500]
	}
	if detail != "" {
		appErr.Detail = fmt.Sprintf("下游返回: %s", detail)
	}

	c.AbortWithStatusJSON(appErr.HTTPStatus, appErr)
}

func jsonMarshal(v interface{}) ([]byte, error) {
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n"), err
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…(已截断)"
}
