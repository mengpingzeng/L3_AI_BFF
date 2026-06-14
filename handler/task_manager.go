package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	c1 "clawstudios/l1_ai_releaser/services/c1_publisher"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/claw-studio/L3_AI_BFF/pkg/idgen"
	"github.com/gin-gonic/gin"
)

type TaskManager struct {
	db              *sql.DB
	mu              sync.Mutex
	queue           []string
	runningTasks    map[string]*AutoPublishJob
	maxSlots        int
	dispatcherStop  chan struct{}

	lastSlotFullLog time.Time
	lastNoTaskLog   time.Time

	*AutoPublishManager
}

type autoPublishTaskRow struct {
	TaskID        string
	UserID        string
	AccountIDs    string
	Platform      string
	WorkID        string
	SkillID       string
	Topic         string
	NovelName     string
	VolumeName    string
	ChapterNumber  int
	BookInfoSet    bool
	Status         string
	RecoverableAt  time.Time
}

func NewTaskManager(db *sql.DB, sessionMgrURL, workflowURL, accountURL, skillRegistryURL, a1BaseURL string, fanqieAdapter *c1.FanqiePublishAdapter, qimaoAdapter *c1.QimaoPublishAdapter, maxSlots int) *TaskManager {
	if maxSlots < 1 {
		maxSlots = 2
	}
	fanqiePlatform := NewFanqiePlatform(fanqieAdapter)
	qimaoPlatform := NewQimaoPlatform(qimaoAdapter)
	platforms := map[string]NovelPlatform{
		"fanqie": fanqiePlatform,
		"qimao":  qimaoPlatform,
	}
	tm := &TaskManager{
		db:             db,
		queue:          make([]string, 0),
		runningTasks:   make(map[string]*AutoPublishJob),
		maxSlots:       maxSlots,
		dispatcherStop: make(chan struct{}),
	}
	tm.AutoPublishManager = NewAutoPublishManager(sessionMgrURL, workflowURL, accountURL, skillRegistryURL, "", platforms, a1BaseURL)
	fanqiePlatform.SetManager(tm.AutoPublishManager)
	qimaoPlatform.SetManager(tm.AutoPublishManager)
	go tm.dispatchLoop()
	return tm
}

func (tm *TaskManager) Shutdown() {
	close(tm.dispatcherStop)
}

func (tm *TaskManager) CreateTask(uid, role string, req model.AutoPublishStartReq) (string, error) {
	taskID := req.TaskID
	if taskID == "" {
		taskID = idgen.NewTaskID()
	}

	taskInfo, err := tm.fetchTaskInfo(taskID)
	if err != nil {
		log.Printf("[task_manager] CreateTask: task=%s 获取任务信息失败: %v", taskID, err)
		return "", fmt.Errorf("获取任务信息失败: %w", err)
	}

	platform := req.Platform
	if platform == "" {
		platform = taskInfo.Platform
	}
	skillID := req.SkillID
	if skillID == "" {
		skillID = taskInfo.SkillID
	}
	if skillID == "" {
		skillID = "general_fallback_v1"
	}
	topic := req.Topic
	if topic == "" {
		topic = taskInfo.Topic
	}
	novelName := req.NovelName
	if novelName == "" {
		novelName = taskInfo.NovelName
	}
	volumeName := req.VolumeName
	if volumeName == "" {
		volumeName = taskInfo.VolumeName
	}
	chapterNumber := taskInfo.ChapterNumber
	if chapterNumber <= 0 {
		chapterNumber = taskInfo.SessionCount
	}

	accountIDsJSON, _ := json.Marshal(req.Accounts)

	now := time.Now()
	_, err = tm.db.Exec(`
		INSERT INTO auto_publish_task
		(task_id, user_id, account_ids, platform, work_id, skill_id, topic, novel_name, volume_name,
		 chapter_number, status, entry_time, recoverable_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?)
	`, taskID, uid, string(accountIDsJSON), platform, "", skillID, topic, novelName,
		volumeName, chapterNumber, now, now)
	if err != nil {
		log.Printf("[task_manager] CreateTask: task=%s 写入MySQL失败: %v", taskID, err)
		return "", fmt.Errorf("写入 MySQL 失败: %w", err)
	}

	tm.mu.Lock()
	tm.queue = append(tm.queue, taskID)
	tm.mu.Unlock()

	log.Printf("[task_manager] task=%s 已入队 user=%s platform=%s", taskID, uid, platform)

	go tm.tryDispatch()

	return taskID, nil
}

func (tm *TaskManager) dispatchLoop() {
	log.Printf("[task_manager] dispatchLoop 启动, maxSlots=%d", tm.maxSlots)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-tm.dispatcherStop:
			log.Printf("[task_manager] dispatchLoop 已停止")
			return
		case <-ticker.C:
			tm.tryDispatch()
		}
	}
}

func (tm *TaskManager) tryDispatch() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(tm.runningTasks) >= tm.maxSlots {
		now := time.Now()
		if now.Sub(tm.lastSlotFullLog) > 30*time.Second {
			log.Printf("[task_manager] dispatch: 槽位已满 running=%d/%d, 等待释放", len(tm.runningTasks), tm.maxSlots)
			tm.lastSlotFullLog = now
		}
		return
	}

	row := tm.db.QueryRow(`
		SELECT task_id, user_id, account_ids, platform, work_id, skill_id, topic, novel_name,
			   volume_name, chapter_number, book_info_set, recoverable_at
		FROM auto_publish_task
		WHERE status = 'queued'
		ORDER BY entry_time ASC
		LIMIT 1
	`)

	var t autoPublishTaskRow
	err := row.Scan(&t.TaskID, &t.UserID, &t.AccountIDs, &t.Platform, &t.WorkID, &t.SkillID,
		&t.Topic, &t.NovelName, &t.VolumeName, &t.ChapterNumber, &t.BookInfoSet, &t.RecoverableAt)
	if err == sql.ErrNoRows {
		now := time.Now()
		if now.Sub(tm.lastNoTaskLog) > 30*time.Second {
			log.Printf("[task_manager] dispatch: 无可调度任务 (running=%d/%d, queued=%d)", len(tm.runningTasks), tm.maxSlots, len(tm.queue))
			tm.lastNoTaskLog = now
		}
		return
	}
	if err != nil {
		log.Printf("[task_manager] 查询可调度任务失败: %v", err)
		return
	}

	if t.RecoverableAt.After(time.Now()) {
		tm.db.Exec(`UPDATE auto_publish_task SET entry_time=UTC_TIMESTAMP() WHERE task_id=?`, t.TaskID)
		tm.removeFromQueueLocked(t.TaskID)
		tm.queue = append(tm.queue, t.TaskID)
		log.Printf("[task_manager] task=%s 仍在冷却期 recoverable_at=%s, 移至队尾",
			t.TaskID, t.RecoverableAt.Format(time.RFC3339))
		return
	}

	tm.db.Exec(`UPDATE auto_publish_task SET status='running' WHERE task_id=?`, t.TaskID)

	job := tm.buildJob(&t)
	tm.runningTasks[t.TaskID] = job
	tm.removeFromQueueLocked(t.TaskID)

	log.Printf("[task_manager] task=%s 派遣成功, 当前运行数=%d/%d", t.TaskID, len(tm.runningTasks), tm.maxSlots)

	go tm.autoPublishLoopWrapped(job)
}

func (tm *TaskManager) buildJob(t *autoPublishTaskRow) *AutoPublishJob {
	stopCtx, stopCancel := context.WithCancel(context.Background())

	var accountIDs []string
	if t.AccountIDs != "" {
		json.Unmarshal([]byte(t.AccountIDs), &accountIDs)
	}

	accounts, err := tm.resolveAccounts(t.UserID, "", t.Platform, accountIDs)
	if err != nil {
		log.Printf("[task_manager] buildJob: task=%s 解析账号失败: %v", t.TaskID, err)
	}

	job := &AutoPublishJob{
		TaskID:        t.TaskID,
		UserID:        t.UserID,
		Platform:      t.Platform,
		Accounts:      accounts,
		SkillID:       t.SkillID,
		Topic:         t.Topic,
		NovelName:     t.NovelName,
		VolumeName:    t.VolumeName,
		ChapterNumber: t.ChapterNumber,
		Status:        "running",
		WorkID:        t.WorkID,
		stopCtx:       stopCtx,
		stopCancel:    stopCancel,
		finishCh:      make(chan struct{}, 1),
		createdAt:     time.Now(),
		BookInfoSet:   t.BookInfoSet,
		retryCount:    0,
	}
	job.onExit = func(j *AutoPublishJob, newStatus string) {
		tm.exitLoop(j, newStatus)
	}
	job.onExitRequeue = func(j *AutoPublishJob, err error) {
		tm.exitLoopAndRequeue(j, err)
	}
	return job
}

func (tm *TaskManager) autoPublishLoopWrapped(job *AutoPublishJob) {
	tm.AutoPublishManager.autoPublishLoop(job)
}

type contextInterface interface {
	Done() <-chan struct{}
	Err() error
	Deadline() (time.Time, bool)
	Value(key interface{}) interface{}
}

func (tm *TaskManager) exitLoopAndRequeue(job *AutoPublishJob, err error) {
	now := time.Now()
	recoverableAt := now.Add(24 * time.Hour)

	job.mu.Lock()
	chapterNumber := job.ChapterNumber
	job.mu.Unlock()

	errMsg := err.Error()
	tm.db.Exec(`
		UPDATE auto_publish_task
		SET status='queued',
			entry_time=?,
			last_executed_at=?,
			recoverable_at=?,
			chapter_number=?,
			error_message=?,
			updated_at=UTC_TIMESTAMP()
		WHERE task_id=?
	`, now, now, recoverableAt, chapterNumber, truncateStr(errMsg, 1000), job.TaskID)

	tm.mu.Lock()
	delete(tm.runningTasks, job.TaskID)
	tm.queue = append(tm.queue, job.TaskID)
	tm.mu.Unlock()

	log.Printf("[task_manager] task=%s 退出并重新入队, recoverable_at=%s, 原因: %v",
		job.TaskID, recoverableAt.Format(time.RFC3339), err)

	go tm.tryDispatch()
}

func (tm *TaskManager) exitLoop(job *AutoPublishJob, newStatus string) {
	tm.db.Exec(`
		UPDATE auto_publish_task SET status=?, last_executed_at=UTC_TIMESTAMP(), updated_at=UTC_TIMESTAMP()
		WHERE task_id=?
	`, newStatus, job.TaskID)

	tm.mu.Lock()
	delete(tm.runningTasks, job.TaskID)
	if newStatus == "deleted" {
		tm.removeFromQueueLocked(job.TaskID)
	}
	tm.mu.Unlock()

	log.Printf("[task_manager] task=%s 退出循环, 新状态=%s", job.TaskID, newStatus)

	go tm.tryDispatch()
}

func (tm *TaskManager) removeFromQueue(taskID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.removeFromQueueLocked(taskID)
}

func (tm *TaskManager) removeFromQueueLocked(taskID string) {
	for i, id := range tm.queue {
		if id == taskID {
			tm.queue = append(tm.queue[:i], tm.queue[i+1:]...)
			return
		}
	}
}

func (tm *TaskManager) RecoverFromMySQL() error {
	rows, err := tm.db.Query(`
		SELECT task_id, user_id, platform, work_id, skill_id, topic, novel_name,
			   volume_name, chapter_number, book_info_set, status
		FROM auto_publish_task
		WHERE status IN ('running', 'queued')
	`)
	if err != nil {
		return fmt.Errorf("查询恢复任务失败: %w", err)
	}
	defer rows.Close()

	var recovered int
	for rows.Next() {
		var t autoPublishTaskRow
		if err := rows.Scan(&t.TaskID, &t.UserID, &t.Platform, &t.WorkID, &t.SkillID,
			&t.Topic, &t.NovelName, &t.VolumeName, &t.ChapterNumber, &t.BookInfoSet, &t.Status); err != nil {
			log.Printf("[task_manager] 扫描恢复行失败: %v", err)
			continue
		}

		if t.Status == "running" {
			tm.db.Exec(`UPDATE auto_publish_task SET status='queued', recoverable_at=UTC_TIMESTAMP() WHERE task_id=?`, t.TaskID)
			t.Status = "queued"
		}

		tm.queue = append(tm.queue, t.TaskID)
		recovered++
	}

	log.Printf("[task_manager] 恢复 %d 个任务", recovered)
	return nil
}

func (tm *TaskManager) StopTaskHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.AutoPublishStopReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		tm.mu.Lock()
		job, running := tm.runningTasks[req.TaskID]
		tm.mu.Unlock()

		if running {
			job.stopCancel()
			log.Printf("[task_manager] Stop: task=%s 运行中, 已发送停止信号", req.TaskID)
		} else {
			tm.db.Exec(`UPDATE auto_publish_task SET status='stopped' WHERE task_id=? AND status='queued'`, req.TaskID)
			log.Printf("[task_manager] Stop: task=%s 未在运行, 直接标记stopped", req.TaskID)
		}
		model.Success(c, gin.H{"task_id": req.TaskID, "status": "stopped"})
	}
}

func (tm *TaskManager) RestartTaskHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.AutoPublishRestartReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		now := time.Now()
		tm.db.Exec(`
			UPDATE auto_publish_task
			SET status='queued', entry_time=?, recoverable_at=?, error_message=NULL
			WHERE task_id=? AND status='stopped'
		`, now, now, req.TaskID)

		log.Printf("[task_manager] Restart: task=%s 已重新入队", req.TaskID)

		tm.mu.Lock()
		tm.queue = append(tm.queue, req.TaskID)
		tm.mu.Unlock()

		go tm.tryDispatch()
		model.Success(c, gin.H{"task_id": req.TaskID, "status": "queued"})
	}
}

func (tm *TaskManager) DeleteTaskHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.AutoPublishDeleteReq
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}

		tm.mu.Lock()
		job, running := tm.runningTasks[req.TaskID]
		tm.mu.Unlock()

		if running {
			job.onExit = nil
			job.onExitRequeue = nil
			job.stopCancel()
			log.Printf("[task_manager] Delete: task=%s 运行中, 已发送停止信号", req.TaskID)
		} else {
			log.Printf("[task_manager] Delete: task=%s 未在运行, 直接标记deleted", req.TaskID)
		}

		tm.db.Exec(`UPDATE auto_publish_task SET status='deleted' WHERE task_id=?`, req.TaskID)

		tm.mu.Lock()
		delete(tm.runningTasks, req.TaskID)
		tm.removeFromQueueLocked(req.TaskID)
		tm.mu.Unlock()

		go tm.tryDispatch()
		model.Success(c, gin.H{"task_id": req.TaskID, "status": "deleted"})
	}
}

func (tm *TaskManager) GetTaskStatusHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID := c.Query("task_id")
		if taskID == "" {
			model.Error(c, model.ErrInvalidParam.WithDetail("task_id is required"))
			return
		}

		row := tm.db.QueryRow(`
			SELECT status, chapter_number, entry_time, last_executed_at, recoverable_at, error_message
			FROM auto_publish_task WHERE task_id=?
		`, taskID)

		var status string
		var chapterNumber int
		var entryTime, lastExecutedAt, recoverableAt sql.NullTime
		var errorMessage sql.NullString
		if err := row.Scan(&status, &chapterNumber, &entryTime, &lastExecutedAt, &recoverableAt, &errorMessage); err == sql.ErrNoRows {
			model.Error(c, model.ErrNotFound.WithDetail("任务不存在"))
			return
		} else if err != nil {
			model.Error(c, model.ErrInternal.WithDetail(err.Error()))
			return
		}

		queuePosition := -1
		if status == "queued" {
			queuePosition = queryAutoPublishQueuePosition(tm.db, taskID)
		}

		var running bool
		tm.mu.Lock()
		_, running = tm.runningTasks[taskID]
		tm.mu.Unlock()

		data := gin.H{
			"task_id":                     taskID,
			"auto_publish_status":         status,
			"running":                     running,
			"chapter_number":              chapterNumber,
			"auto_publish_queue_position": queuePosition,
		}
		if entryTime.Valid {
			data["auto_publish_entry_time"] = entryTime.Time.Format(time.RFC3339)
		}
		if lastExecutedAt.Valid {
			data["last_executed_at"] = lastExecutedAt.Time.Format(time.RFC3339)
		}
		if recoverableAt.Valid {
			data["recoverable_at"] = recoverableAt.Time.Format(time.RFC3339)
		}
		if errorMessage.Valid && errorMessage.String != "publish daily limit: daily_limit_reached" {
			data["auto_publish_error_message"] = errorMessage.String
		}

		model.Success(c, data)
	}
}

// queryAutoPublishQueuePosition 与任务列表 enrich 逻辑一致：按 entry_time 排序的 queued 位次。
func queryAutoPublishQueuePosition(db *sql.DB, taskID string) int {
	rows, err := db.Query(`SELECT task_id FROM auto_publish_task WHERE status='queued' ORDER BY entry_time ASC`)
	if err != nil {
		return -1
	}
	defer rows.Close()
	pos := 0
	for rows.Next() {
		pos++
		var tid string
		if rows.Scan(&tid) == nil && tid == taskID {
			return pos
		}
	}
	return -1
}

func (tm *TaskManager) GetQueueHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		model.Success(c, gin.H{
			"running_count": len(tm.runningTasks),
			"max_slots":     tm.maxSlots,
		})
	}
}

func (tm *TaskManager) SetMaxSlotsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			MaxSlots int `json:"max_slots"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			model.Error(c, model.ErrInvalidParam.WithDetail("请求体格式错误"))
			return
		}
		if req.MaxSlots < 1 {
			req.MaxSlots = 1
		}

		tm.mu.Lock()
		tm.maxSlots = req.MaxSlots
		running := len(tm.runningTasks)
		tm.mu.Unlock()

		model.Success(c, gin.H{"max_slots": req.MaxSlots, "current_running": running})
	}
}
