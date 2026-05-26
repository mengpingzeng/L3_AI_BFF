package middleware

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

const LoggerKey = "session_logger"

type ginLogBodyWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *ginLogBodyWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func Logging() gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID, sessionID := extractGinIDs(c)
		bodyBytes, _ := readGinBody(c)

		bodyTaskID, bodySessionID := logging.ExtractIDsFromBody(bodyBytes)
		if bodyTaskID != "" {
			taskID = bodyTaskID
		}
		if bodySessionID != "" {
			sessionID = bodySessionID
		}

		logger := logging.NewLogger("BFFGateway",
			logging.WithTaskID(taskID),
			logging.WithSessionID(sessionID),
		)

		logger.LogRequest(c.Request, bodyBytes)

		c.Set(LoggerKey, logger)
		c.Request = c.Request.WithContext(logging.NewContext(c.Request.Context(), logger))

		blw := &ginLogBodyWriter{
			ResponseWriter: c.Writer,
			body:           &bytes.Buffer{},
		}
		c.Writer = blw

		start := time.Now()
		c.Next()
		duration := time.Since(start)

		respBody := blw.body.Bytes()

		respTaskID, respSessionID := logging.ExtractIDsFromRespBody(respBody)
		if respTaskID != "" || respSessionID != "" {
			logger.UpdateIDs(respTaskID, respSessionID)
		}

		logger.LogResponse(c.Writer.Status(), respBody, duration)

		if len(c.Errors) > 0 {
			logger.Error(logging.ErrInternal, "Gin request errors: %v", c.Errors.String())
		}

		logger.Close()
	}
}

func GetBFFLogger(c *gin.Context) *logging.Logger {
	if logger, ok := c.Get(LoggerKey); ok {
		if l, ok := logger.(*logging.Logger); ok {
			return l
		}
	}
	return nil
}

func extractGinIDs(c *gin.Context) (taskID, sessionID string) {
	taskID = c.Param("tid")
	if taskID == "" {
		taskID = c.Param("task_id")
	}
	sessionID = c.Param("sid")
	if sessionID == "" {
		sessionID = c.Param("session_id")
	}
	if taskID == "" {
		taskID = "_unassigned"
	}
	if sessionID == "" {
		sessionID = "_task"
	}
	return
}

func readGinBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
	return body, nil
}
