package handler

import (
	"encoding/json"
	"strconv"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

type bookContentRaw struct {
	SessionID     string `json:"session_id"`
	Status        string `json:"status"`
	DraftVersion  int    `json:"draft_version"`
	VolumeName    string `json:"volume_name"`
	ChapterNumber int    `json:"chapter_number"`
	CreatedAt     string `json:"created_at"`
}

type draftRaw struct {
	Draft        string `json:"draft"`
	ChapterTitle string `json:"chapter_title"`
	DraftVersion int    `json:"draft_version"`
}

func BookGetContent(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)
		taskID := c.Param("tid")
		volumeName := c.Query("volume_name")
		chapterStr := c.Query("chapter_number")

		if volumeName == "" {
			if logger != nil {
				logger.Error(logging.ErrInvalidParam, "book/get_content: volume_name is required task=%s", taskID)
			}
			model.Error(c, model.ErrInvalidParam.WithDetail("卷名不能为空"))
			return
		}
		chapterNum, err := strconv.Atoi(chapterStr)
		if err != nil || chapterStr == "" {
			if logger != nil {
				logger.Error(logging.ErrInvalidParam, "book/get_content: invalid chapter_number=%s task=%s", chapterStr, taskID)
			}
			model.Error(c, model.ErrInvalidParam.WithDetail("章号格式不合法"))
			return
		}

		sessionsData, err := doDownstreamGet(sessionMgrURL + "/api/task/" + taskID + "/sessions")
		if err != nil {
			if logger != nil {
				logger.Error(logging.ErrInternal, "book/get_content: get sessions failed task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		var sessionsResp struct {
			Sessions []bookContentRaw `json:"sessions"`
		}
		if err := json.Unmarshal(sessionsData, &sessionsResp); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError, "book/get_content: parse sessions failed task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		var matchedSessionID string
		var matchedCreatedAt string
		for _, s := range sessionsResp.Sessions {
			if s.VolumeName == volumeName && s.ChapterNumber == chapterNum {
				matchedSessionID = s.SessionID
				matchedCreatedAt = s.CreatedAt
				break
			}
		}

		if matchedSessionID == "" {
			if logger != nil {
				logger.Warn(logging.WarnServiceDegraded, "book/get_content: chapter not found task=%s vol=%s ch=%d", taskID, volumeName, chapterNum)
			}
			model.Error(c, model.ErrNotFound.WithDetail("章节不存在"))
			return
		}

		draftData, err := doDownstreamGet(sessionMgrURL + "/api/session/" + matchedSessionID + "/draft")
		if err != nil {
			if logger != nil {
				logger.Error(logging.ErrInternal, "book/get_content: get draft failed session=%s err=%v", matchedSessionID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		var draft draftRaw
		if err := json.Unmarshal(draftData, &draft); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError, "book/get_content: parse draft failed session=%s err=%v", matchedSessionID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		uid, _ := c.Get("uid")
		tid, _ := c.Get(model.TraceIDKey)
		if logger != nil {
			logger.Info("book/get_content: chapter fetched task=%s vol=%s ch=%d session=%s uid=%v", taskID, volumeName, chapterNum, matchedSessionID, uid)
		}

		c.JSON(200, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data: gin.H{
				"task_id":        taskID,
				"volume_name":    volumeName,
				"chapter_number": chapterNum,
				"session_id":     matchedSessionID,
				"chapter_title":  draft.ChapterTitle,
				"content":        draft.Draft,
				"draft_version":  draft.DraftVersion,
				"created_at":     matchedCreatedAt,
			},
			TraceID: tid.(string),
		})
	}
}
