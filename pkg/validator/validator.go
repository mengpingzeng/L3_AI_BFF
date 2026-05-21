package validator

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	reSkillID      = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
	reTaskID       = regexp.MustCompile(`^task_[a-z0-9]{12}$`)
	reSessionID    = regexp.MustCompile(`^(sess_)?[a-z0-9]+$`)
	reAccID        = regexp.MustCompile(`^acc_[a-z0-9]+$`)
	validPlatforms = map[string]bool{"fanqie": true, "xhs": true, "wechat": true, "yuewen": true, "zhulang": true}

	ModelList = map[string]bool{
		"deepseek-chat":     true,
		"deepseek-reasoner": true,
		"hy3-preview":       true,
		"deepseek/deepseek-chat":      true,
		"deepseek/deepseek-v4-flash":  true,
		"deepseek/deepseek-v4-pro":    true,
		"hy3/hy3-preview":             true,
		"opencode/big-pickle":         true,
		"opencode/nemotron-3-super-free": true,
	}
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ValidationResult struct {
	Valid  bool         `json:"valid"`
	Errors []FieldError `json:"errors,omitempty"`
}

func (r *ValidationResult) Add(field, msg string) {
	r.Valid = false
	r.Errors = append(r.Errors, FieldError{Field: field, Message: msg})
}

func ValidateCreateTask(topic, skillID, model, platform string) ValidationResult {
	var r ValidationResult
	r.Valid = true

	if strings.TrimSpace(topic) == "" {
		r.Add("topic", "主题不能为空")
	} else if utf8.RuneCountInString(topic) > 500 {
		r.Add("topic", "主题不能超过500个字符")
	} else if containsControl(topic) {
		r.Add("topic", "主题包含非法字符")
	}

	if !reSkillID.MatchString(skillID) {
		r.Add("skill_id", "写作风格 ID 格式不合法")
	}

	if !isValidModel(model) {
		r.Add("model", "不支持的模型: "+model)
	}

	if platform == "" {
		r.Add("platform", "发布平台不能为空")
	} else if !validPlatforms[platform] {
		r.Add("platform", "不支持的平台: "+platform)
	}

	return r
}

func ValidateCreateSession(taskID string) ValidationResult {
	var r ValidationResult
	r.Valid = true

	if !reTaskID.MatchString(taskID) {
		r.Add("task_id", "任务 ID 格式不合法")
	}

	return r
}

func ValidateSendMessage(text string) ValidationResult {
	var r ValidationResult
	r.Valid = true

	if strings.TrimSpace(text) == "" {
		r.Add("text", "消息不能为空")
	} else if utf8.RuneCountInString(text) > 4000 {
		r.Add("text", "消息不能超过4000个字符")
	}

	return r
}

func ValidateAccountIDs(ids []string) ValidationResult {
	var r ValidationResult
	r.Valid = true

	for _, id := range ids {
		if !reAccID.MatchString(id) {
			r.Add("account_ids", "账号 ID 格式不合法: "+id)
		}
	}

	return r
}

func ValidatePagination(page, size int) ValidationResult {
	var r ValidationResult
	r.Valid = true

	if page < 1 {
		r.Add("page", "页码必须为正整数")
	}
	if size < 1 || size > 100 {
		r.Add("size", "每页条数必须在 1~100 之间")
	}

	return r
}

func ValidateTimelineQuery(cursor string, limit int) ValidationResult {
	var r ValidationResult
	r.Valid = true

	if limit < 1 || limit > 100 {
		r.Add("limit", "条数必须在 1~100 之间")
	}

	return r
}

func containsControl(s string) bool {
	for _, r := range s {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return true
		}
	}
	return false
}

func isValidModel(m string) bool {
	return ModelList[m]
}

func IsValidSessionID(sid string) bool {
	return reSessionID.MatchString(sid)
}

func IsValidTaskID(tid string) bool {
	return reTaskID.MatchString(tid)
}
