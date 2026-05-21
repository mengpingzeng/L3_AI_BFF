package middleware

import (
	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.GetHeader("X-Trace-ID")
		if tid == "" {
			tid = "trc_" + uuid.New().String()[:12]
		}
		c.Set(model.TraceIDKey, tid)
		c.Header("X-Trace-ID", tid)
		c.Next()
	}
}
