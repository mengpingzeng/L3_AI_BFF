package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

var httpClient = &http.Client{
	Timeout: 600 * time.Second,
}

func injectProxyHeaders(c *gin.Context, req *http.Request, bodyData map[string]interface{}) {
	if tid, ok := c.Get(model.TraceIDKey); ok {
		req.Header.Set("X-Trace-ID", tid.(string))
	}
	if uid, ok := c.Get("uid"); ok {
		req.Header.Set("X-User-ID", uid.(string))
	}
	if role, ok := c.Get("role"); ok {
		req.Header.Set("X-User-Role", role.(string))
	}

	if tid := c.Param("tid"); tid != "" {
		req.Header.Set("X-Task-ID", tid)
	}
	if sid := c.Param("sid"); sid != "" {
		req.Header.Set("X-Session-ID", sid)
	}

	if bodyData != nil {
		if v, ok := bodyData["task_id"].(string); ok && v != "" {
			req.Header.Set("X-Task-ID", v)
		}
		if v, ok := bodyData["taskId"].(string); ok && v != "" {
			req.Header.Set("X-Task-ID", v)
		}
		if v, ok := bodyData["session_id"].(string); ok && v != "" {
			req.Header.Set("X-Session-ID", v)
		}
	}
}

func Forward(c *gin.Context, upstreamURL string, bodyData map[string]interface{}) ([]byte, int, error) {
	jsonBody, err := jsonMarshal(bodyData)
	if err != nil {
		return nil, 0, err
	}

	logger := middleware.GetBFFLogger(c)

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		if logger != nil {
			logger.Error(logging.ErrProxyError, "创建下游请求失败: %s %s — %v", "POST", upstreamURL, err)
		}
		return nil, 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	injectProxyHeaders(c, req, bodyData)

	start := time.Now()
	resp, err := httpClient.Do(req)
	duration := time.Since(start)

	if err != nil {
		if logger != nil {
			logger.LogProxyCall(upstreamURL, "POST", jsonBody, 0, nil, duration, err)
		}
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if logger != nil {
			logger.LogProxyCall(upstreamURL, "POST", jsonBody, 0, nil, duration, err)
		}
		return nil, 0, err
	}

	if logger != nil {
		logger.LogProxyCall(upstreamURL, "POST", jsonBody, resp.StatusCode, respBody, duration, nil)
	}
	return respBody, resp.StatusCode, nil
}

func ForwardGet(c *gin.Context, upstreamURL string) ([]byte, int, error) {
	logger := middleware.GetBFFLogger(c)

	if c.Request.URL.RawQuery != "" && !strings.Contains(upstreamURL, "?") {
		upstreamURL += "?" + c.Request.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		if logger != nil {
			logger.Error(logging.ErrProxyError, "创建下游请求失败: GET %s — %v", upstreamURL, err)
		}
		return nil, 0, err
	}

	injectProxyHeaders(c, req, nil)

	start := time.Now()
	resp, err := httpClient.Do(req)
	duration := time.Since(start)

	if err != nil {
		if logger != nil {
			logger.LogProxyCall(upstreamURL, "GET", nil, 0, nil, duration, err)
		}
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if logger != nil {
			logger.LogProxyCall(upstreamURL, "GET", nil, resp.StatusCode, respBody, duration, err)
		}
		return nil, 0, err
	}

	if logger != nil {
		logger.LogProxyCall(upstreamURL, "GET", nil, resp.StatusCode, respBody, duration, nil)
	}
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
	case 409:
		return model.ErrConflict
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

	errCopy := &model.AppError{
		Code:       appErr.Code,
		Message:    appErr.Message,
		HTTPStatus: appErr.HTTPStatus,
	}
	if tid != nil {
		errCopy.TraceID = tid.(string)
	}

	if len(respBody) > 0 {
		var downstream struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(respBody, &downstream) == nil && downstream.Message != "" {
			errCopy.Message = downstream.Message
		} else {
			detail := string(respBody)
			if len(detail) > 500 {
				detail = detail[:500]
			}
			errCopy.Detail = fmt.Sprintf("下游返回: %s", detail)
		}
	}

	logger := middleware.GetBFFLogger(c)
	if logger != nil {
		logger.Warn(logging.WarnServiceDegraded, "下游 %s 返回错误 [%d]: %s", service, statusCode, errCopy.Message)
	}

	c.AbortWithStatusJSON(errCopy.HTTPStatus, errCopy)
}

func jsonMarshal(v interface{}) ([]byte, error) {
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n"), err
}

func ForwardDelete(c *gin.Context, upstreamURL string) ([]byte, int, error) {
	logger := middleware.GetBFFLogger(c)

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodDelete, upstreamURL, nil)
	if err != nil {
		return nil, 0, err
	}

	injectProxyHeaders(c, req, nil)

	start := time.Now()
	resp, err := httpClient.Do(req)
	duration := time.Since(start)
	if err != nil {
		if logger != nil {
			logger.LogProxyCall(upstreamURL, "DELETE", nil, 0, nil, duration, err)
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	if logger != nil {
		logger.LogProxyCall(upstreamURL, "DELETE", nil, resp.StatusCode, body, duration, nil)
	}
	return body, resp.StatusCode, nil
}
