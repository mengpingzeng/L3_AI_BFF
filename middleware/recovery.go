package middleware

import (
	"net/http"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/gin-gonic/gin"
)

func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				tid, _ := c.Get(model.TraceIDKey)
				err := model.ErrInternal.WithTraceID(tid.(string))
				c.AbortWithStatusJSON(http.StatusInternalServerError, err)
			}
		}()

		c.Next()
	}
}
