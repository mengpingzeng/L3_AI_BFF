package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	c1 "clawstudios/l1_ai_releaser/services/c1_publisher"
	_ "github.com/go-sql-driver/mysql"

	"github.com/claw-studio/L3_AI_BFF/config"
	"github.com/claw-studio/L3_AI_BFF/handler"
	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/router"
)

func main() {
	cfg := config.Load()

	middleware.InitJWT(cfg.JWTSecret)

	var db *sql.DB
	if cfg.DB_DSN != "" {
		var err error
		db, err = sql.Open("mysql", cfg.DB_DSN)
		if err != nil {
			log.Fatalf("MySQL 连接失败: %v", err)
		}
		defer db.Close()
		if err := db.Ping(); err != nil {
			log.Printf("MySQL 不可达: %v (TaskManager 将使用内存模式)", err)
			db = nil
		} else {
			db.SetMaxOpenConns(10)
			db.SetMaxIdleConns(5)
			db.SetConnMaxLifetime(5 * time.Minute)
			log.Println("MySQL 连接成功")
		}
	}

	fanqieAdapter := c1.NewFanqiePublishAdapter(c1.AdapterConfig{
		ScriptPath: cfg.FanqieScript,
		Timeout:    600 * time.Second,
	})

	autoPubMgr := handler.NewAutoPublishManager(cfg.SessionMgrURL, cfg.WorkflowURL, cfg.A1AccountURL, cfg.SkillRegistryURL, cfg.StoppedTasksFile, fanqieAdapter, cfg.A1BaseURL)
	var taskMgr *handler.TaskManager
	if db != nil {
		taskMgr = handler.NewTaskManager(db, cfg.SessionMgrURL, cfg.WorkflowURL,
			cfg.A1AccountURL, cfg.SkillRegistryURL, cfg.A1BaseURL, fanqieAdapter, 2)
		if err := taskMgr.RecoverFromMySQL(); err != nil {
			log.Printf("TaskManager 恢复失败: %v", err)
		}
	} else {
		log.Println("无 MySQL 连接, TaskManager 未启用, 使用兼容层")
	}

	r := router.Setup(cfg, autoPubMgr, taskMgr)

	srv := &http.Server{
		Addr:        ":" + cfg.Port,
		Handler:     r,
		ReadTimeout: 30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout: 120 * time.Second,
	}

	go func() {
		log.Printf("BFF 接待员 启动成功，监听端口: %s", cfg.Port)
		log.Printf("健康检查: http://localhost:%s/healthz", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("正在优雅关闭...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("服务关闭失败: %v", err)
	}

	fmt.Println("服务已安全关闭")
}
