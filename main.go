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

	"github.com/claw-studio/L3_AI_BFF/config"
	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/claw-studio/L3_AI_BFF/proxy"
	"github.com/claw-studio/L3_AI_BFF/router"
)

func main() {
	cfg := config.Load()

	middleware.InitJWT(cfg.JWTSecret)

	wsProxy := proxy.NewWSProxy(cfg.SessionMgrURL, cfg.WorkflowURL)
	r := router.Setup(cfg, wsProxy)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
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
	proxy.ActiveConns.GracefulShutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("服务关闭失败: %v", err)
	}

	fmt.Println("服务已安全关闭")
}
