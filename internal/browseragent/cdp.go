package browseragent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// cdpProbeTimeout 是探测系统浏览器调试端口的单次 HTTP 请求超时。
const cdpProbeTimeout = 2 * time.Second

// cdpCallTimeout 限制单条 CDP 命令的等待时间，避免浏览器无响应时长期占用调用方 Context。
const cdpCallTimeout = 5 * time.Second

// cdpVersionBodyLimit 限制 /json/version 响应体读取长度，避免异常响应耗尽内存。
const cdpVersionBodyLimit = 1 << 20

// cdpCookieMethods 是按优先级尝试的浏览器级 Cookie 读取命令。
// Storage.getCookies 位于浏览器域，Network.getAllCookies 作为旧版本兼容回退。
var cdpCookieMethods = []string{"Storage.getCookies", "Network.getAllCookies"}

// cdpCookie 是 CDP 返回的最小 Cookie 视图；仅用于判定新 x5sec，不得写入日志或持久化。
type cdpCookie struct {
	// Name 是 Cookie 名称，例如 x5sec。
	Name string `json:"name"`
	// Value 是 Cookie 值；调用方不得记录完整值。
	Value string `json:"value"`
	// Domain 是 Cookie 作用域，用于确认验证结果属于目标站点。
	Domain string `json:"domain"`
}

// cdpCookieClient 定义按本机调试端口读取系统浏览器 Cookie 的最小能力。
type cdpCookieClient interface {
	// Cookies 读取指定回环调试端口上浏览器的全部 Cookie；Context 取消必须立刻中止等待。
	Cookies(ctx context.Context, port int) ([]cdpCookie, error)
}

// cdpWebSocketCookieClient 通过浏览器级 CDP WebSocket 读取 Cookie，不执行页面脚本或自动交互。
type cdpWebSocketCookieClient struct {
	// client 用于解析 /json/version；为空时使用带探测超时的默认客户端。
	client *http.Client
}

// Cookies 解析调试端口地址并读取浏览器全部 Cookie。
func (c cdpWebSocketCookieClient) Cookies(ctx context.Context, port int) ([]cdpCookie, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("%w: 调试端口 %d 超出范围", ErrInvalidRequest, port)
	}
	// endpoint 是浏览器级 CDP WebSocket 地址；读取失败表示浏览器尚未就绪或端口不可用。
	endpoint, err := resolveCDPEndpoint(ctx, c.httpClient(), port)
	if err != nil {
		return nil, err
	}
	return readCDPCookies(ctx, endpoint)
}

// httpClient 返回用于探测调试端口的 HTTP 客户端。
func (c cdpWebSocketCookieClient) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return &http.Client{Timeout: cdpProbeTimeout}
}

// resolveCDPEndpoint 通过本机回环 /json/version 读取浏览器级 WebSocket 调试地址。
func resolveCDPEndpoint(ctx context.Context, client *http.Client, port int) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: cdpProbeTimeout}
	}
	// requestURL 固定使用回环地址，避免连接到非本机调试端口。
	requestURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/json/version"
	// request 是带调用方 Context 的探测请求，取消时必须立刻中止。
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", fmt.Errorf("构造 CDP 版本请求失败: %w", err)
	}
	// response 是浏览器返回的版本信息；非 200 表示调试端口尚未开放。
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("系统浏览器调试端口不可用: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("系统浏览器调试端口返回状态 %d", response.StatusCode)
	}
	// payload 只保留 WebSocket 调试地址字段。
	var payload struct {
		// WebSocketDebuggerURL 是浏览器级 CDP 连接地址。
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	// err 是版本响应体解码失败的错误，表示调试端口返回的不是预期 JSON 而非地址不可用。
	if err := json.NewDecoder(io.LimitReader(response.Body, cdpVersionBodyLimit)).Decode(&payload); err != nil {
		return "", fmt.Errorf("解析 CDP 版本响应失败: %w", err)
	}
	// endpoint 是去除空白后的调试地址；只接受 WebSocket 方案。
	endpoint := strings.TrimSpace(payload.WebSocketDebuggerURL)
	if !strings.HasPrefix(endpoint, "ws://") && !strings.HasPrefix(endpoint, "wss://") {
		return "", fmt.Errorf("CDP 未返回可用的 WebSocket 调试地址")
	}
	return endpoint, nil
}

// readCDPCookies 连接浏览器级调试地址并按优先级尝试读取全部 Cookie。
func readCDPCookies(ctx context.Context, endpoint string) ([]cdpCookie, error) {
	// conn 是浏览器级 CDP 连接，关闭由本函数负责。
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("连接系统浏览器调试端口失败: %w", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "manual verification finished") }()
	// attemptErr 保存最后一条命令错误，用于两种命令都不可用时返回诊断。
	var attemptErr error
	// method 是当前尝试的浏览器级 Cookie 读取命令。
	for _, method := range cdpCookieMethods {
		// cookies、callErr 是当前命令返回的 Cookie 集合及其失败原因。
		cookies, callErr := callCDPCookieMethod(ctx, conn, method)
		if callErr == nil {
			return cookies, nil
		}
		attemptErr = callErr
	}
	return nil, attemptErr
}

// callCDPCookieMethod 执行单条 Cookie 读取命令并解析 cookie 列表。
func callCDPCookieMethod(ctx context.Context, conn *websocket.Conn, method string) ([]cdpCookie, error) {
	// callCtx、cancel 为单条命令提供独立超时，避免浏览器无响应时阻塞调用方。
	callCtx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	// payload 是固定 id 的 CDP 命令，便于按 id 匹配响应并跳过事件通知。
	payload, err := json.Marshal(map[string]any{"id": 1, "method": method})
	if err != nil {
		return nil, fmt.Errorf("构造 CDP 命令 %s 失败: %w", method, err)
	}
	// err 是 CDP 命令写入失败的错误，涵盖连接被关闭与 callCtx 超时两种情况。
	if err := conn.Write(callCtx, websocket.MessageText, payload); err != nil {
		return nil, fmt.Errorf("发送 CDP 命令 %s 失败: %w", method, err)
	}
	for {
		// data 是当前 CDP 消息；读取错误包含超时和连接关闭。
		_, data, readErr := conn.Read(callCtx)
		if readErr != nil {
			return nil, fmt.Errorf("读取 CDP 命令 %s 响应失败: %w", method, readErr)
		}
		// envelope 是 CDP 响应信封；不带匹配 id 的事件通知必须跳过。
		var envelope struct {
			// ID 是命令关联标识；零值表示事件通知。
			ID int64 `json:"id"`
			// Result 是命令原始结果，按命令单独解析。
			Result json.RawMessage `json:"result"`
			// Error 是 CDP 返回的命令级错误。
			Error *struct {
				// Message 是浏览器给出的错误说明，不包含凭证明文。
				Message string `json:"message"`
			} `json:"error"`
		}
		// unmarshalErr 是消息解码失败的错误；与 id 不匹配一并视为事件通知并继续读取下一条消息。
		if unmarshalErr := json.Unmarshal(data, &envelope); unmarshalErr != nil || envelope.ID != 1 {
			continue
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("CDP 命令 %s 返回错误: %s", method, envelope.Error.Message)
		}
		// result 是命令返回的 Cookie 列表集合。
		var result struct {
			// Cookies 是浏览器当前作用域内的全部 Cookie。
			Cookies []cdpCookie `json:"cookies"`
		}
		// unmarshalErr 是命令结果解析失败的错误，表示浏览器返回结构与 Cookie 列表不兼容。
		if unmarshalErr := json.Unmarshal(envelope.Result, &result); unmarshalErr != nil {
			return nil, fmt.Errorf("解析 CDP 命令 %s 结果失败: %w", method, unmarshalErr)
		}
		return result.Cookies, nil
	}
}

// freshX5secValue 从浏览器 Cookie 中筛选出不在已知集合内的非空 x5sec 值。
// known 保存调用方已持有的 x5sec 快照；仅返回全新值才能证明本次人工验证真实生效。
func freshX5secValue(cookies []cdpCookie, known []string) (string, bool) {
	// knownValues 保存归一化后的已持有值集合，避免重复计数。
	knownValues := make(map[string]struct{}, len(known))
	// value 是当前遍历到的已知 x5sec 值。
	for _, value := range known {
		// trimmed 是当前已知值去除首尾空白后的形式；空串表示调用方传入的占位值，不参与比对。
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			knownValues[trimmed] = struct{}{}
		}
	}
	// cookie 是当前待判定的浏览器 Cookie。
	for _, cookie := range cookies {
		if !strings.EqualFold(strings.TrimSpace(cookie.Name), "x5sec") {
			continue
		}
		// value 是去除空白后的 Cookie 值；空值和已持有值都不算成功。
		value := strings.TrimSpace(cookie.Value)
		if value == "" {
			continue
		}
		// exists 表示该 Cookie 值已经在调用方持有的快照中出现，属于旧值而非本次验证产生的新值。
		if _, exists := knownValues[value]; exists {
			continue
		}
		return value, true
	}
	return "", false
}
