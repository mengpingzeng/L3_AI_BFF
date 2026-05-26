package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/model"
	"clawstudios/pkg/logging"
	"github.com/gin-gonic/gin"
)

type bookVolume struct {
	VolumeName   string        `json:"volume_name"`
	ChapterCount int           `json:"chapter_count"`
	Chapters     []bookChapter `json:"chapters"`
}

type bookChapter struct {
	ChapterNumber int    `json:"chapter_number"`
	SessionID     string `json:"session_id"`
	Title         string `json:"title,omitempty"`
	Status        string `json:"status"`
	DraftVersion  int    `json:"draft_version"`
	CreatedAt     string `json:"created_at"`
	ArchivedAt    string `json:"archived_at,omitempty"`
}

func BookGetInfo(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := middleware.GetBFFLogger(c)
		taskID := c.Param("tid")

		taskData, err := doDownstreamGet(sessionMgrURL + "/api/task/" + taskID)
		if err != nil {
			if logger != nil {
				logger.Error(logging.ErrNotFound, "book/get_info: get task failed: task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrNotFound.WithDetail("任务不存在"))
			return
		}

		var task struct {
			NovelName     string `json:"novel_name"`
			VolumeName    string `json:"volume_name"`
			ChapterNumber int    `json:"chapter_number"`
			SessionCount  int    `json:"session_count"`
		}
		if err := json.Unmarshal(taskData, &task); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError, "book/get_info: parse task failed: task=%s err=%v raw=%s",
					taskID, err, truncate(taskData, 500))
			}
			model.Error(c, model.ErrInternal)
			return
		}

		sessionsData, err := doDownstreamGet(sessionMgrURL + "/api/task/" + taskID + "/sessions")
		if err != nil {
			if logger != nil {
				logger.Error(logging.ErrDatabaseError, "book/get_info: get sessions failed: task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		var sessionsResp struct {
			Sessions []bookChapterRaw `json:"sessions"`
		}
		if err := json.Unmarshal(sessionsData, &sessionsResp); err != nil {
			if logger != nil {
				logger.Error(logging.ErrMarshalError, "book/get_info: parse sessions failed: task=%s err=%v", taskID, err)
			}
			model.Error(c, model.ErrInternal)
			return
		}

		volumes := buildVolumeTree(sessionsResp.Sessions)

		hasUnclassified := false
		for i := range volumes {
			if volumes[i].VolumeName == "" {
				hasUnclassified = true
				if task.VolumeName != "" {
					volumes[i].VolumeName = task.VolumeName
				}
			}
		}

		if hasUnclassified {
			merged := mergeUnclassified(volumes)
			volumes = merged
		}

		totalChapters := 0
		for _, v := range volumes {
			totalChapters += v.ChapterCount
		}

		uid, _ := c.Get("uid")
		tid, _ := c.Get(model.TraceIDKey)
		if logger != nil {
			logger.Info("book/get_info: returned task=%s volumes=%d chapters=%d uid=%v", taskID, len(volumes), totalChapters, uid)
		}

		c.JSON(200, model.APIResponse{
			Code:    0,
			Message: "ok",
			Data: gin.H{
				"task_id":        taskID,
				"novel_name":     task.NovelName,
				"total_volumes":  len(volumes),
				"total_chapters": totalChapters,
				"volumes":        volumes,
			},
			TraceID: tid.(string),
		})
	}
}

type bookChapterRaw struct {
	SessionID     string `json:"session_id"`
	Status        string `json:"status"`
	DraftVersion  int    `json:"draft_version"`
	VolumeName    string `json:"volume_name"`
	ChapterNumber int    `json:"chapter_number"`
	CreatedAt     string `json:"created_at"`
	ArchivedAt    string `json:"archived_at,omitempty"`
}

func buildVolumeTree(sessions []bookChapterRaw) []bookVolume {
	volMap := make(map[string][]bookChapter)
	volOrder := make([]string, 0)
	seen := make(map[string]bool)

	chapters := make([]bookChapter, 0, len(sessions))
	for _, s := range sessions {
		chapters = append(chapters, bookChapter{
			ChapterNumber: s.ChapterNumber,
			SessionID:     s.SessionID,
			Status:        s.Status,
			DraftVersion:  s.DraftVersion,
			CreatedAt:     s.CreatedAt,
			ArchivedAt:    s.ArchivedAt,
		})
	}
	sort.Slice(chapters, func(i, j int) bool {
		return chapters[i].DraftVersion < chapters[j].DraftVersion
	})

	for i, s := range sessions {
		volName := s.VolumeName
		if volName == "" {
			volName = ""
		}
		if !seen[volName] {
			seen[volName] = true
			volOrder = append(volOrder, volName)
		}
		volMap[volName] = append(volMap[volName], chapters[i])
	}

	sort.Slice(volOrder, func(i, j int) bool {
		if volOrder[i] == "" {
			return true
		}
		if volOrder[j] == "" {
			return false
		}
		return volOrder[i] < volOrder[j]
	})

	result := make([]bookVolume, 0, len(volOrder))
	for _, vn := range volOrder {
		result = append(result, bookVolume{
			VolumeName:   vn,
			ChapterCount: len(volMap[vn]),
			Chapters:     volMap[vn],
		})
	}

	return result
}

func mergeUnclassified(volumes []bookVolume) []bookVolume {
	var unclassified []bookChapter
	var classified []bookVolume
	for _, v := range volumes {
		if v.VolumeName == "" || v.VolumeName == volumes[0].VolumeName && volumes[0].VolumeName == "" {
			unclassified = append(unclassified, v.Chapters...)
		} else {
			classified = append(classified, v)
		}
	}
	if len(unclassified) == 0 {
		return volumes
	}
	for i := range classified {
		if classified[i].VolumeName != "" {
			classified[i].Chapters = append(unclassified, classified[i].Chapters...)
			classified[i].ChapterCount = len(classified[i].Chapters)
			sort.Slice(classified[i].Chapters, func(a, b int) bool {
				return classified[i].Chapters[a].DraftVersion < classified[i].Chapters[b].DraftVersion
			})
			return classified
		}
	}
	return volumes
}

var bffHTTPClient = &http.Client{Timeout: 30 * time.Second}

func doDownstreamGet(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := bffHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
