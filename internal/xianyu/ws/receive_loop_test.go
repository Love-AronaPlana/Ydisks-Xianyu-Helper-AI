package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startWSEchoServer 启动一个本地 WS 服务：升级后发送一条同步推送消息，再读取并忽略 ACK，
// 最后关闭连接。返回服务 URL。用于驱动 ReceiveLoop 的消息分发测试。
// startWSEchoServer 封装开始WSEchoServer业务协调。
func startWSEchoServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// c、err 用于本次流程后续判断的c、err
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// 构造同步推送帧：body.syncPushPackage.data[0].data = base64(payload)。
		b64 := base64.StdEncoding.EncodeToString([]byte(payload))
		// frame 用于本次流程后续判断的frame
		frame := map[string]any{
			"lwp":     "/s/sync",
			"headers": map[string]any{"mid": "m1", "sid": "s1"},
			"body": map[string]any{"syncPushPackage": map[string]any{
				"data": []any{map[string]any{"data": b64}},
			}},
		}
		// raw 用于本次流程后续判断的原始
		raw, _ := json.Marshal(frame)
		if // err 用于本次流程后续判断的err
		err := c.Write(r.Context(), websocket.MessageText, raw); err != nil {
			return
		}
		// 读掉 ACK 后关闭（触发客户端 Read 返回错误，结束 ReceiveLoop）。
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		_, _, _ = c.Read(ctx)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wsURL 把 httptest 的 http:// URL 转成 ws://。
func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestReceiveLoop_DecodesSyncPayload 验证 ReceiveLoop 收到同步推送帧后：
// 解码 base64+JSON → 调用 onMessage → 回 ACK（服务端能读到）→ 连接关闭后退出。
// TestReceiveLoop_DecodesSyncPayload 封装TestReceiveLoopDecodesSync请求载荷业务协调。
func TestReceiveLoop_DecodesSyncPayload(t *testing.T) {
	// payload 用于本次流程后续判断的请求载荷
	payload := `{"event":"paid","order_id":"o1"}`
	// srv 用于本次流程后续判断的srv
	srv := startWSEchoServer(t, payload)

	// dialCtx、dialCancel 用于本次流程后续判断的dialCtx、dial取消
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed、err 用于本次流程后续判断的dialed、err
	dialed, _, err := websocket.Dial(dialCtx, wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// conn 用于本次流程后续判断的conn
	conn := newConn(dialed, Config{}, nilLogger())

	// got 用于本次流程后续判断的got
	var got map[string]any
	// loopDone 用于本次流程后续判断的loopDone
	loopDone := make(chan error, 1)
	// ctx、cancel 用于本次流程后续判断的ctx、cancel
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		loopDone <- conn.ReceiveLoop(ctx, func(decrypted map[string]any) {
			got = decrypted
		})
	}()

	select {
	case <-loopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ReceiveLoop 未在超时内退出")
	}
	if got == nil || got["event"] != "paid" || got["order_id"] != "o1" {
		t.Fatalf("onMessage 未收到解码结果: %#v", got)
	}
}

// TestReceiveLoop_NonJSONSkipped 非 JSON 消息应被跳过，不回调 onMessage，循环继续。
func TestReceiveLoop_NonJSONSkipped(t *testing.T) {
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// c、err 用于本次流程后续判断的c、err
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// 先发一条非 JSON，再发一条合法同步推送。
		c.Write(r.Context(), websocket.MessageText, []byte("not-json"))
		// b64 用于本次流程后续判断的b64
		b64 := base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))
		// frame 用于本次流程后续判断的frame
		frame := map[string]any{
			"lwp":     "/s/sync",
			"headers": map[string]any{"mid": "m2"},
			"body":    map[string]any{"syncPushPackage": map[string]any{"data": []any{map[string]any{"data": b64}}}},
		}
		// raw 用于本次流程后续判断的原始
		raw, _ := json.Marshal(frame)
		c.Write(r.Context(), websocket.MessageText, raw)
		// ctx、cancel 用于本次流程后续判断的ctx、cancel
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		_, _, _ = c.Read(ctx) // ACK
	}))
	defer srv.Close()

	// dialCtx、dialCancel 用于本次流程后续判断的dialCtx、dial取消
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed、err 用于本次流程后续判断的dialed、err
	dialed, _, err := websocket.Dial(dialCtx, wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// conn 用于本次流程后续判断的conn
	conn := newConn(dialed, Config{}, nilLogger())
	// got 用于本次流程后续判断的got
	var got map[string]any
	// loopDone 用于本次流程后续判断的loopDone
	loopDone := make(chan error, 1)
	// ctx、cancel 用于本次流程后续判断的ctx、cancel
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		loopDone <- conn.ReceiveLoop(ctx, func(decrypted map[string]any) { got = decrypted })
	}()
	<-loopDone
	if got == nil || got["ok"] != true {
		t.Fatalf("应跳过非 JSON 并处理合法帧: %#v", got)
	}
}

// TestReceiveLoop_DispatchesEverySyncPayload 验证同一同步帧内的每个条目都会被处理：
// 帧内按平台顺序包含 state_changed、paid 与无法解密的 not-base64；两个有效事件必须按帧内顺序
// 调用 onMessage，坏条目只产生一次 decrypt_failed 记录且不阻断同帧其它条目，整帧只回一次 ACK。
func TestReceiveLoop_DispatchesEverySyncPayload(t *testing.T) {
	// ackCount 是服务端读到的客户端确认帧数量，用于断言整帧只回一次 ACK。
	ackCount := 0
	// ackHeaders 是首个 ACK 回传的 headers，用于验证 ACK 复用原始帧 headers 而非新造字段。
	var ackHeaders map[string]any
	// serverDone 在服务端写入帧、读取确认并关闭连接后关闭；测试据此同步服务端计数。
	serverDone := make(chan struct{})
	// srv 是本地 WebSocket 服务，只推送一帧带三个条目的同步消息。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// c、err 分别是本次升级得到的服务端连接与升级错误。
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			close(serverDone)
			return
		}
		// 连接关闭后调用方才能认为 ackCount 已稳定，因此关闭动作与 serverDone 一起放在延迟函数中。
		defer func() {
			_ = c.Close(websocket.StatusNormalClosure, "")
			close(serverDone)
		}()
		// entries 是本帧的三个条目：前两条是 base64+JSON 的有效事件，第三条无法 base64 解码。
		entries := []any{
			map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(`{"event":"state_changed"}`))},
			map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(`{"event":"paid"}`))},
			map[string]any{"data": "not-base64"},
		}
		// frame 是多条目同步推送帧；同时带 lwp 与 headers 才会被客户端读循环判定为 Push。
		frame := map[string]any{
			"lwp":     "/s/sync",
			"headers": map[string]any{"mid": "multi-mid", "sid": "multi-sid"},
			"body":    map[string]any{"syncPushPackage": map[string]any{"data": entries}},
		}
		// raw 是写入客户端的平台帧字节。
		raw, _ := json.Marshal(frame)
		if // writeErr 是本次写入平台帧的错误；写入失败时客户端收不到任何消息。
		writeErr := c.Write(r.Context(), websocket.MessageText, raw); writeErr != nil {
			return
		}
		// ctx、cancel 限制第一次 ACK 读取窗口，避免客户端异常时测试挂死。
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if // data、readErr 分别是第一条客户端消息的载荷与读取错误。
		_, data, readErr := c.Read(ctx); readErr == nil {
			ackCount++
			// ack 是解析后的客户端确认对象，用于核对回传状态码与 headers。
			var ack map[string]any
			if // unmarshalErr 是确认帧的 JSON 解析错误；解析失败时仅跳过 headers 断言。
			unmarshalErr := json.Unmarshal(data, &ack); unmarshalErr == nil {
				ackHeaders, _ = ack["headers"].(map[string]any)
			}
		}
		// 再用短窗口读取一次：客户端若为同一帧重复回 ACK，这里会读到第二条消息。
		dupCtx, dupCancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
		if // dupErr 是短窗口读取的结果；超时表示没有重复 ACK。
		_, _, dupErr := c.Read(dupCtx); dupErr == nil {
			ackCount++
		}
		dupCancel()
	}))
	t.Cleanup(srv.Close)

	// dialCtx、dialCancel 限制握手时间。
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed、_, dialErr 分别是客户端连接、握手响应与握手错误。
	dialed, _, dialErr := websocket.Dial(dialCtx, wsURL(srv), nil)
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// events 按派发顺序记录 onMessage 收到的事件名，用于断言帧内顺序没有被重排或丢弃。
	events := make([]string, 0, 2)
	// decryptFailed 统计 recorder 报告的解密失败次数，用于断言坏条目只影响自身。
	decryptFailed := 0
	// conn 是本次测试的客户端连接。
	conn := newConn(dialed, Config{}, nilLogger())
	// rec 是帧记录器回调：只统计入方向解密失败，其它状态与本用例断言无关。
	rec := func(direction, rawText, parsedJSON, parseStatus, errMsg string) {
		if direction == "in" && parseStatus == "decrypt_failed" {
			decryptFailed++
		}
	}
	conn.SetRecorder(rec)
	// loopDone 在 ReceiveLoop 退出后收到其返回值；服务端关闭连接即触发退出。
	loopDone := make(chan error, 1)
	// ctx、cancel 限制 ReceiveLoop 生命周期，避免连接异常时测试挂死。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		loopDone <- conn.ReceiveLoop(ctx, func(decrypted map[string]any) {
			// event 是当前解码消息携带的业务事件名；缺失时记录空字符串以便断言失败时可见。
			event, _ := decrypted["event"].(string)
			events = append(events, event)
		})
	}()
	// 依次等待客户端循环退出与服务端处理完成，channel 接收同时保证两侧计数对测试 goroutine 可见。
	<-loopDone
	<-serverDone

	if len(events) != 2 || events[0] != "state_changed" || events[1] != "paid" {
		t.Fatalf("onMessage 派发顺序 = %#v，期望 [state_changed paid]", events)
	}
	if decryptFailed != 1 {
		t.Fatalf("decrypt_failed 记录次数 = %d，期望 1（坏条目只影响自身）", decryptFailed)
	}
	if ackCount != 1 {
		t.Fatalf("ACK 次数 = %d，期望 1（整帧只确认一次）", ackCount)
	}
	if ackHeaders["mid"] != "multi-mid" || ackHeaders["sid"] != "multi-sid" {
		t.Fatalf("ACK headers = %#v，期望原帧 headers", ackHeaders)
	}
}

// TestHeartbeatLoop_ContextCancel HeartbeatLoop 应在 ctx 取消时及时退出。
func TestHeartbeatLoop_ContextCancel(t *testing.T) {
	// srv 用于本次流程后续判断的srv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// c、err 用于本次流程后续判断的c、err
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// 持续读，忽略心跳。
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		for {
			if // err 用于本次流程后续判断的err
			_, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	// dialCtx、dialCancel 用于本次流程后续判断的dialCtx、dial取消
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	// dialed、err 用于本次流程后续判断的dialed、err
	dialed, _, err := websocket.Dial(dialCtx, wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialed.CloseNow()
	dialed.SetReadLimit(8 << 20)

	// conn 用于本次流程后续判断的conn
	conn := newConn(dialed, Config{}, nilLogger())
	// ctx、cancel 用于本次流程后续判断的ctx、cancel
	ctx, cancel := context.WithCancel(context.Background())
	// loopDone 用于本次流程后续判断的loopDone
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- conn.HeartbeatLoop(ctx, 50*time.Millisecond)
	}()
	// 让心跳发几次再取消。
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case // err 用于本次流程后续判断的err
	err := <-loopDone:
		if err != nil && err != context.Canceled {
			t.Fatalf("HeartbeatLoop 退出 err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HeartbeatLoop 未在取消后退出")
	}
}

// nilLogger 返回一个丢弃所有输出的 slog.Logger，避免测试输出刷屏。
func nilLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
