package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	c1 "clawstudios/l1_ai_releaser/services/c1_publisher"

	"github.com/claw-studio/L3_AI_BFF/config"
	"github.com/claw-studio/L3_AI_BFF/handler"
	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/router"
)

func main() {
	cfg := config.Load()

	middleware.InitJWT(cfg.JWTSecret)

	fanqieAdapter := c1.NewFanqiePublishAdapter(c1.AdapterConfig{
		ScriptPath: cfg.FanqieScript,
		Timeout:    600 * time.Second,
	})

	autoPubMgr := handler.NewAutoPublishManager(cfg.SessionMgrURL, cfg.WorkflowURL, cfg.A1AccountURL, cfg.SkillRegistryURL, cfg.StoppedTasksFile, fanqieAdapter, cfg.A1BaseURL)
	r := router.Setup(cfg, autoPubMgr)

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
