package model

type CreateTaskReq struct {
	Topic         string   `json:"topic"`
	NovelName     string   `json:"name,omitempty"`
	Platform      string   `json:"platform"`
	AccountIDs    []string `json:"account_ids"`
	SkillID       string   `json:"skill_id"`
	SkillVersion  string   `json:"skillVer"`
	Model         string   `json:"model"`
	IsAutoPublish bool     `json:"is_auto_publish"`
}

type CreateSessionReq struct {
	TaskID        string `json:"task_id"`
	SkillID       string `json:"skillId"`
	SkillVer      string `json:"skillVer"`
	Model         string `json:"model"`
	Topic         string `json:"topic"`
	Platform      string `json:"platform"`
	AccountID     string `json:"accountId"`
	NovelName     string `json:"novel_name,omitempty"`
	ChapterNumber int    `json:"chapter_number,omitempty"`
}

type SendMessageReq struct {
	Text            string `json:"text"`
	DraftVersion    int    `json:"draft_version"`
	TargetSessionID string `json:"target_session_id,omitempty"`
	Mode            string `json:"mode,omitempty"`
}

type PublishReq struct {
	DraftVersion  int      `json:"draft_version"`
	SessionID     string   `json:"sessionId"`
	Platform      string   `json:"platform"`
	Accounts      []string `json:"accounts"`
	SkillID       string   `json:"skillId"`
	Topic         string   `json:"topic"`
	NovelName     string   `json:"novelName"`
	Title         string   `json:"title"`
	VolumeName    string   `json:"volumeName"`
	ChapterNumber int      `json:"chapterNumber"`
}

type TaskListQuery struct {
	Page int    `form:"page"`
	Size int    `form:"size"`
	Q    string `form:"q"`
}

type TimelineQuery struct {
	Cursor string `form:"cursor"`
	Limit  int    `form:"limit"`
}

type AutoPublishStartReq struct {
	TaskID     string   `json:"task_id"`
	Platform   string   `json:"platform"`
	Accounts   []string `json:"accounts,omitempty"`
	SkillID    string   `json:"skill_id,omitempty"`
	Model      string   `json:"model,omitempty"`
	Topic      string   `json:"topic,omitempty"`
	NovelName  string   `json:"novel_name,omitempty"`
	VolumeName string   `json:"volume_name,omitempty"`
}

type AutoPublishStopReq struct {
	TaskID string `json:"task_id"`
	UserID string `json:"user_id"`
}

type AutoPublishQueueStatusReq struct {
	TaskID string `form:"task_id"`
}

type AutoPublishFinishReq struct {
	TaskID string `json:"task_id"`
	UserID string `json:"user_id"`
}

type AutoPublishRestartReq struct {
	TaskID string `json:"task_id"`
}

type AutoPublishDeleteReq struct {
	TaskID string `json:"task_id"`
}

type AllocSkillReq struct {
	Platform string `json:"platform" binding:"required"`
	Theme    string `json:"theme,omitempty"`
	Style    string `json:"style,omitempty"`
}
