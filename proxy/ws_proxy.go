package proxy

import (
	"context"
	"fmt"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer cancel()
		for {
			msgType, data, err := frontConn.ReadMessage()
			if err != nil {
				return
			}
			if err := downConn.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		defer cancel()
		for {
			msgType, data, err := downConn.ReadMessage()
			if err != nil {
				return
			}
			if err := frontConn.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	<-ctx.Done()
	wg.Wait()
	return nil
}
