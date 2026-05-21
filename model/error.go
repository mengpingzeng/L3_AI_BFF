package model

import "fmt"

type AppError struct {
	Code       int    `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"-"`
	Detail     any    `json:"detail,omitempty"`
	TraceID    string `json:"trace_id"`
}

func (e *AppError) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

func (e *AppError) WithDetail(detail any) *AppError {
	return &AppError{
		Code:       e.Code,
		Message:    e.Message,
		HTTPStatus: e.HTTPStatus,
		Detail:     detail,
	}
}

func (e *AppError) WithTraceID(tid string) *AppError {
	e.TraceID = tid
	return e
}

var (
	ErrUnauthorized       = &AppError{Code: 1001, Message: "未登录或登录已过期", HTTPStatus: 401}
	ErrInvalidToken       = &AppError{Code: 1002, Message: "令牌无效", HTTPStatus: 401}
	ErrInvalidParam       = &AppError{Code: 1003, Message: "请求参数不合法", HTTPStatus: 400}
	ErrNotFound           = &AppError{Code: 1004, Message: "资源不存在", HTTPStatus: 404}
	ErrMethodNotAllowed   = &AppError{Code: 1005, Message: "不支持的请求方法", HTTPStatus: 405}
	ErrRateLimited        = &AppError{Code: 1006, Message: "请求太频繁，请稍后重试", HTTPStatus: 429}

	ErrInternal           = &AppError{Code: 9001, Message: "服务器内部错误", HTTPStatus: 500}
	ErrUpstreamUnavailable = &AppError{Code: 9002, Message: "下游服务暂不可用", HTTPStatus: 502}
	ErrUpstreamTimeout     = &AppError{Code: 9003, Message: "下游服务响应超时", HTTPStatus: 504}
	ErrRequestTooLarge     = &AppError{Code: 9004, Message: "请求体过大", HTTPStatus: 413}
)
