package model

import (
	"github.com/gin-gonic/gin"
)

type APIResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	TraceID string `json:"trace_id"`
}

func Success(c *gin.Context, data any) {
	tid, _ := c.Get(TraceIDKey)
	c.JSON(200, APIResponse{
		Code:    0,
		Message: "ok",
		Data:    data,
		TraceID: tid.(string),
	})
}

func Error(c *gin.Context, err *AppError) {
	tid, _ := c.Get(TraceIDKey)
	if tid != nil {
		err.TraceID = tid.(string)
	}
	c.AbortWithStatusJSON(err.HTTPStatus, err)
}
