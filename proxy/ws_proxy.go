package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/claw-studio/L3_AI_BFF/middleware"
	"github.com/gorilla/websocket"
)

var wsDialer = websocket.Dialer{
	HandshakeTimeout: 5 * time.Second,
}

type WSProxy struct {
	SessionMgrURL string
	WorkflowURL   string
	PingInterval  time.Duration
	WriteTimeout  time.Duration
}

func NewWSProxy(sessionMgrURL, workflowURL string) *WSProxy {
	return &WSProxy{
		SessionMgrURL: sessionMgrURL,
		WorkflowURL:   workflowURL,
		PingInterval:  30 * time.Second,
		WriteTimeout:  10 * time.Second,
	}
}

func (p *WSProxy) Proxy(w http.ResponseWriter, r *http.Request, upstreamURLStr string, uid string) error {
	frontConn, err := middleware.WSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("升级前端 WS 失败: %w", err)
	}
	defer frontConn.Close()

	downstreamHeader := http.Header{}
	downstreamHeader.Set("X-User-ID", uid)
	if tid := r.URL.Query().Get("trace_id"); tid != "" {
		downstreamHeader.Set("X-Trace-ID", tid)
	}

	parsedURL, err := url.Parse(upstreamURLStr)
	if err != nil {
		frontConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(1011, "内部错误"))
		return fmt.Errorf("解析下游 URL 失败: %w", err)
	}

	downConn, _, err := wsDialer.Dial(parsedURL.String(), downstreamHeader)
	if err != nil {
		frontConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(1011, "下游服务不可用"))
		return fmt.Errorf("连接下游 WS 失败: %w", err)
	}
	defer downConn.Close()

	// 设置 Pong 处理器：收到 pong 后延长读取 deadline（只对前端连接设置）
	keepAlive := p.PingInterval + p.WriteTimeout
	frontConn.SetReadDeadline(time.Now().Add(keepAlive))
	frontConn.SetPongHandler(func(string) error {
		frontConn.SetReadDeadline(time.Now().Add(keepAlive))
		return nil
	})
	// 本地 L2 连接不设 deadline，避免 L2 未响应 Ping 导致超时断连

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(3)

	// 前端 → 下游
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			msgType, data, err := frontConn.ReadMessage()
			if err != nil {
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Printf("[WSProxy] 读前端消息失败: %v", err)
				}
				return
			}
			// 收到任何消息，延长读取 deadline（保活）
			frontConn.SetReadDeadline(time.Now().Add(keepAlive))
			if err := downConn.WriteMessage(msgType, data); err != nil {
				log.Printf("[WSProxy] 写下游消息失败: %v", err)
				return
			}
		}
	}()

	// 下游 → 前端
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			msgType, data, err := downConn.ReadMessage()
			if err != nil {
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Printf("[WSProxy] 读下游消息失败: %v", err)
				}
				return
			}
			downConn.SetReadDeadline(time.Now().Add(keepAlive))
			if err := frontConn.WriteMessage(msgType, data); err != nil {
				log.Printf("[WSProxy] 写前端消息失败: %v", err)
				return
			}
		}
	}()

	// Ping/Pong 心跳 goroutine：每 PingInterval 向前端发送 Ping（保活浏览器侧连接）
	go func() {
		defer wg.Done()
		defer cancel()
		ticker := time.NewTicker(p.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				frontConn.SetWriteDeadline(time.Now().Add(p.WriteTimeout))
				if err := frontConn.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Printf("[WSProxy] Ping 前端失败: %v", err)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	<-ctx.Done()
	wg.Wait()
	return nil
}
