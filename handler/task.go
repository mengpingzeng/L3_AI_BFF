package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/pkg/idgen"
	"github.com/claw-studio/L3_AI_BFF/pkg/validator"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/gin-gonic/gin"
)

func CreateTask(sessionMgrURL string, autoPubMgr *AutoPublishManager, taskMgr *TaskManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.CreateTaskReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		vr := validator.ValidateCreateTask(req.Topic, req.SkillID, req.Model, req.Platform)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		if len(req.AccountIDs) > 0 {
			avr := validator.ValidateAccountIDs(req.AccountIDs)
			if !avr.Valid {
				model.Error(c, model.ErrInvalidParam.WithDetail(avr.Errors))
				return
			}
		}

		tid, _ := c.Get(model.TraceIDKey)
		taskID := idgen.NewTaskID()
		uid, _ := c.Get("uid")
		role, _ := c.Get("role")

		body := map[string]interface{}{
			"task_id":    taskID,
			"topic":      req.Topic,
			"platform":   req.Platform,
			"skill_id":   req.SkillID,
			"model":      req.Model,
			"uid":        uid,
			"novel_name": req.NovelName,
		}
		if len(req.AccountIDs) > 0 {
			body["account_id"] = req.AccountIDs[0]
		}
		respBody, statusCode, err := proxy.Forward(c, sessionMgrURL+"/api/task/create", body)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		if statusCode >= 300 {
			proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
				c.Header("Content-Type", "application/json")
				c.String(200, string(data))
			})
			return
		}

		autoPublishStarted := false
		if req.IsAutoPublish {
			autoReq := model.AutoPublishStartReq{
				TaskID:    taskID,
				Platform:  req.Platform,
				Accounts:  req.AccountIDs,
				SkillID:   req.SkillID,
				Model:     req.Model,
				Topic:     req.Topic,
				NovelName: req.NovelName,
			}
			if taskMgr != nil {
				if _, err := taskMgr.CreateTask(uid.(string), role.(string), autoReq); err != nil {
					log.Printf("[create_task] task=%s 入队失败: %v", taskID, err)
				} else {
					autoPublishStarted = true
				}
			} else if autoPubMgr != nil {
				if err := autoPubMgr.StartAutoPublishInternal(uid.(string), role.(string), autoReq); err != nil {
					log.Printf("[create_task] task=%s 自动发布启动失败: %v", taskID, err)
				} else {
					autoPublishStarted = true
				}
			}
		}

		model.Success(c, gin.H{
			"task_id":              taskID,
			"trace_id":             tid,
			"uid":                  uid,
			"is_auto_publish":      req.IsAutoPublish,
			"auto_publish_started": autoPublishStarted,
		})
	}
}

func TaskUpdate(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		var body map[string]interface{}
		if err := c.ShouldBindJSON(&body); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}
		url := sessionMgrURL + "/api/task/" + tid + "/update"
		respBody, statusCode, err := proxy.Forward(c, url, body)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func DeleteTask(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		url := sessionMgrURL + "/api/task/" + tid
		respBody, statusCode, err := proxy.ForwardDelete(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func TaskSessions(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		url := sessionMgrURL + "/api/task/" + tid + "/sessions"
		respBody, statusCode, err := proxy.ForwardGet(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func GetTask(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		url := sessionMgrURL + "/api/task/" + tid
		respBody, statusCode, err := proxy.ForwardGet(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}
		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func ListTask(listURL string, autoPubMgr *AutoPublishManager, dashboardURL string, taskMgr *TaskManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var q model.TaskListQuery
		if err := c.ShouldBindQuery(&q); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("查询参数格式错误"))
			return
		}

		if q.Page == 0 {
			q.Page = 1
		}
		if q.Size == 0 {
			q.Size = 12
		}

		vr := validator.ValidatePagination(q.Page, q.Size)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		queryURL := listURL + "?page=" + intToStr(q.Page) + "&size=" + intToStr(q.Size)
		if q.Q != "" {
			queryURL += "&q=" + url.QueryEscape(q.Q)
		}

		uid, _ := c.Get("uid")
		role, _ := c.Get("role")
		if role != "admin" && uid != nil {
			queryURL += "&uid=" + uid.(string)
		}

		respBody, statusCode, err := proxy.ForwardGet(c, queryURL)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		var tasksJSON []byte
		useProxyFallback := true

		if statusCode >= 200 && statusCode < 300 && autoPubMgr != nil {
			uidFilter := ""
			if role != "admin" {
				if uid != nil {
					uidFilter = uid.(string)
				}
			}
			autoPubMgr.ReloadStoppedTasks()
			smBase := strings.TrimSuffix(listURL, "/api/task/list")
			visible, err := listVisibleTasks(smBase, uidFilter, q.Q, autoPubMgr.IsStopped)
			if err == nil {
				payload, _ := json.Marshal(map[string]interface{}{
					"tasks": visible,
					"total": len(visible),
				})
				tasksJSON = payload
				useProxyFallback = false
			}
		}

		if !useProxyFallback {
			enriched := enrichTasksWithPublishStats(tasksJSON, dashboardURL)
			if taskMgr != nil {
				enriched = enrichTasksWithAutoPublish(enriched, taskMgr)
			}
			enriched = sortTaskListJSON(enriched)
			paged, pageErr := paginateTaskListJSON(enriched, q.Page, q.Size)
			if pageErr == nil {
				c.Header("Content-Type", "application/json")
				c.String(200, paged)
				return
			}
		}

		if useProxyFallback {
			if statusCode >= 300 {
				proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
					c.Header("Content-Type", "application/json")
					c.String(200, string(data))
				})
				return
			}
			tasksJSON = respBody
		}

		enriched := enrichTasksWithPublishStats(tasksJSON, dashboardURL)
		if taskMgr != nil {
			enriched = enrichTasksWithAutoPublish(enriched, taskMgr)
		}
		enriched = sortTaskListJSON(enriched)

		c.Header("Content-Type", "application/json")
		c.String(200, string(enriched))
	}
}

type publishStats struct {
	TotalPosts    int `json:"totalPosts"`
	TotalViews    int `json:"totalViews"`
	TotalLikes    int `json:"totalLikes"`
	TotalComments int `json:"totalComments"`
	TotalShares   int `json:"totalShares"`
}

func enrichTasksWithPublishStats(tasksJSON []byte, dashboardURL string) []byte {
	var resp struct {
		Tasks []map[string]interface{} `json:"tasks"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(tasksJSON, &resp); err != nil || len(resp.Tasks) == 0 {
		return tasksJSON
	}

	taskIDs := make([]string, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		if tid, ok := t["task_id"].(string); ok && tid != "" {
			taskIDs = append(taskIDs, tid)
		}
	}

	if len(taskIDs) == 0 {
		return tasksJSON
	}

	stats := fetchPublishBatchStats(dashboardURL, taskIDs)
	for _, t := range resp.Tasks {
		tid, _ := t["task_id"].(string)
		if s, ok := stats[tid]; ok {
			t["publish_stats"] = map[string]interface{}{
				"totalPosts":    s.TotalPosts,
				"totalViews":    s.TotalViews,
				"totalLikes":    s.TotalLikes,
				"totalComments": s.TotalComments,
				"totalShares":   s.TotalShares,
			}
		}
	}

	result, _ := json.Marshal(resp)
	return result
}

func enrichTasksWithAutoPublish(tasksJSON []byte, taskMgr *TaskManager) []byte {
	var resp struct {
		Tasks []map[string]interface{} `json:"tasks"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(tasksJSON, &resp); err != nil || len(resp.Tasks) == 0 {
		return tasksJSON
	}

	taskIDs := make([]string, 0, len(resp.Tasks))
	taskIndex := make(map[string]int)
	for i, t := range resp.Tasks {
		if tid, ok := t["task_id"].(string); ok && tid != "" {
			taskIDs = append(taskIDs, tid)
			taskIndex[tid] = i
		}
	}
	if len(taskIDs) == 0 {
		return tasksJSON
	}

	query, args, err := sqlxIn(taskIDs)
	if err != nil {
		return tasksJSON
	}
	rows, err := taskMgr.db.Query(query, args...)
	if err != nil {
		return tasksJSON
	}
	defer rows.Close()

	for rows.Next() {
		var tid, status string
		var entryTime time.Time
		var errMsg sql.NullString
		if scanErr := rows.Scan(&tid, &status, &entryTime, &errMsg); scanErr != nil {
			continue
		}
		if idx, ok := taskIndex[tid]; ok {
			resp.Tasks[idx]["auto_publish_status"] = status
			resp.Tasks[idx]["auto_publish_entry_time"] = entryTime.Format(time.RFC3339)
			if errMsg.Valid && errMsg.String != "publish daily limit: daily_limit_reached" {
				resp.Tasks[idx]["auto_publish_error_message"] = errMsg.String
			}
		}
	}

	qRows, qErr := taskMgr.db.Query(`SELECT task_id FROM auto_publish_task WHERE status='queued' ORDER BY entry_time ASC`)
	if qErr == nil {
		defer qRows.Close()
		pos := 0
		for qRows.Next() {
			pos++
			var qTid string
			if qRows.Scan(&qTid) == nil {
				for _, t := range resp.Tasks {
					tid, _ := t["task_id"].(string)
					if tid == qTid && t["auto_publish_status"] == "queued" {
						t["auto_publish_queue_position"] = pos
						break
					}
				}
			}
		}
	}

	filtered := make([]map[string]interface{}, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		if t["auto_publish_status"] != "deleted" {
			filtered = append(filtered, t)
		}
	}
	resp.Tasks = filtered

	result, _ := json.Marshal(resp)
	return result
}

func sortTaskListJSON(tasksJSON []byte) []byte {
	var resp struct {
		Tasks []map[string]interface{} `json:"tasks"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(tasksJSON, &resp); err != nil || len(resp.Tasks) == 0 {
		return tasksJSON
	}
	sortTasksByAutoPublishPriority(resp.Tasks)
	resp.Total = len(resp.Tasks)
	result, _ := json.Marshal(resp)
	return result
}

func paginateTaskListJSON(tasksJSON []byte, page, size int) (string, error) {
	var resp struct {
		Tasks []map[string]interface{} `json:"tasks"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(tasksJSON, &resp); err != nil {
		return "", err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 12
	}
	total := len(resp.Tasks)
	start := (page - 1) * size
	if start >= total {
		resp.Tasks = []map[string]interface{}{}
	} else {
		end := start + size
		if end > total {
			end = total
		}
		resp.Tasks = resp.Tasks[start:end]
	}
	resp.Total = total
	data, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func sortTasksByAutoPublishPriority(tasks []map[string]interface{}) {
	sort.SliceStable(tasks, func(i, j int) bool {
		ti, qi := taskAutoPublishSortTier(tasks[i])
		tj, qj := taskAutoPublishSortTier(tasks[j])
		if ti != tj {
			return ti < tj
		}
		if ti == 1 && qi != qj {
			return qi < qj
		}
		return taskCreatedAtTime(tasks[i]).Before(taskCreatedAtTime(tasks[j]))
	})
}

func taskAutoPublishSortTier(t map[string]interface{}) (tier int, queuePos int) {
	status, _ := t["auto_publish_status"].(string)
	switch status {
	case "running":
		return 0, 0
	case "queued":
		return 1, taskQueuePositionFromMap(t)
	default:
		return 2, 0
	}
}

func taskQueuePositionFromMap(t map[string]interface{}) int {
	switch v := t["auto_publish_queue_position"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 1_000_000
	}
}

func taskCreatedAtTime(t map[string]interface{}) time.Time {
	raw, _ := t["created_at"].(string)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func sqlxIn(ids []string) (string, []interface{}, error) {
	if len(ids) == 0 {
		return "", nil, fmt.Errorf("empty ids")
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("SELECT task_id, status, entry_time, error_message FROM auto_publish_task WHERE task_id IN (%s)", strings.Join(placeholders, ","))
	return query, args, nil
}

func fetchPublishBatchStats(dashboardURL string, taskIDs []string) map[string]publishStats {
	batchURL := strings.TrimRight(dashboardURL, "/") + "/api/dashboard/batch"
	body := map[string]interface{}{
		"taskIds": taskIDs,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil
	}

	req, err := http.NewRequest(http.MethodPost, batchURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	var batchResp struct {
		Stats map[string]publishStats `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&batchResp); err != nil {
		return nil
	}

	return batchResp.Stats
}

func listVisibleTasks(sessionMgrURL, uidFilter, search string, isStopped func(string) bool) ([]map[string]interface{}, error) {
	all, err := fetchAllTasksFromSessionMgr(sessionMgrURL, uidFilter, search)
	if err != nil {
		return nil, err
	}
	visible := make([]map[string]interface{}, 0, len(all))
	for _, t := range all {
		taskID, _ := t["task_id"].(string)
		if taskID != "" && isStopped != nil && isStopped(taskID) {
			continue
		}
		visible = append(visible, t)
	}
	return visible, nil
}

func listVisibleTasksPage(sessionMgrURL, uidFilter, search string, page, size int, isStopped func(string) bool) (string, error) {
	visible, err := listVisibleTasks(sessionMgrURL, uidFilter, search, isStopped)
	if err != nil {
		return "", err
	}
	total := len(visible)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 12
	}
	start := (page - 1) * size
	if start >= total {
		visible = []map[string]interface{}{}
	} else {
		end := start + size
		if end > total {
			end = total
		}
		visible = visible[start:end]
	}
	out := map[string]interface{}{
		"tasks": visible,
		"total": total,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func fetchAllTasksFromSessionMgr(sessionMgrURL, uidFilter, search string) ([]map[string]interface{}, error) {
	listURL := strings.TrimSuffix(sessionMgrURL, "/") + "/api/task/list?page=1&size=10000"
	if uidFilter != "" {
		listURL += "&uid=" + url.QueryEscape(uidFilter)
	}
	if search != "" {
		listURL += "&q=" + url.QueryEscape(search)
	}
	resp, err := http.Get(listURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("session_mgr list tasks: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Tasks []map[string]interface{} `json:"tasks"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Tasks == nil {
		return []map[string]interface{}{}, nil
	}
	return payload.Tasks, nil
}

func GetTaskTimeline(timelineURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		var q model.TimelineQuery
		if err := c.ShouldBindQuery(&q); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("查询参数格式错误"))
			return
		}

		if q.Limit == 0 {
			q.Limit = 50
		}

		vr := validator.ValidateTimelineQuery(q.Cursor, q.Limit)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		uid, _ := c.Get("uid")
		role, _ := c.Get("role")

		queryURL := timelineURL + tid + "/timeline?limit=" + intToStr(q.Limit)
		if q.Cursor != "" {
			queryURL += "&cursor=" + q.Cursor
		}
		if role != "admin" && uid != nil {
			queryURL += "&uid=" + uid.(string)
		}

		respBody, statusCode, err := proxy.ForwardGet(c, queryURL)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func GetTaskPublishList(dashboardURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		if tid == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("task id is required"))
			return
		}

		body := map[string]interface{}{
			"taskId": tid,
			"page":   1,
			"size":   200,
		}

		respBody, statusCode, err := proxy.Forward(c, dashboardURL+"/api/dashboard/query", body)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "dashboard", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func TaskMessages(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		if tid == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("task id is required"))
			return
		}

		url := fmt.Sprintf("%s/api/task/%s/messages", sessionMgrURL, tid)
		respBody, statusCode, err := proxy.ForwardGet(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func ClearTaskMessages(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		if tid == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("task id is required"))
			return
		}

		url := fmt.Sprintf("%s/api/task/%s/messages", sessionMgrURL, tid)
		respBody, statusCode, err := proxy.ForwardDelete(c, url)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}

func TaskMessage(sessionMgrURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tid := c.Param("tid")
		if tid == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("task id is required"))
			return
		}

		var req model.SendMessageReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}
		vr := validator.ValidateSendMessage(req.Text)
		if !vr.Valid {
			model.Error(c, model.ErrInvalidParam.WithDetail(vr.Errors))
			return
		}

		traceID, _ := c.Get(model.TraceIDKey)
		uid, _ := c.Get("uid")
		downstream := map[string]interface{}{
			"text":              req.Text,
			"draft_version":     req.DraftVersion,
			"target_session_id": req.TargetSessionID,
			"mode":              req.Mode,
			"uid":               uid,
			"trace_id":          traceID,
		}

		url := fmt.Sprintf("%s/api/task/%s/message", sessionMgrURL, tid)
		respBody, statusCode, err := proxy.Forward(c, url, downstream)
		if err != nil {
			model.Error(c, model.ErrUpstreamUnavailable.WithDetail(err.Error()))
			return
		}

		proxy.HandleDownstreamResponse(c, respBody, statusCode, "session_mgr", func(c *gin.Context, data []byte) {
			c.Header("Content-Type", "application/json")
			c.String(200, string(data))
		})
	}
}
