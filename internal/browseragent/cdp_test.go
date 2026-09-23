package browseragent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeCDPServer 提供最小浏览器级 CDP 端点：/json/version 返回 WebSocket 地址，
// WebSocket 侧按命令名返回预设 Cookie 或错误，用于确定性验证读取逻辑。
type fakeCDPServer struct {
	// server 是承载版本端点和 WebSocket 端点的测试服务器。
	server *httptest.Server
	// cookiesByMethod 按 CDP 命令名返回 Cookie 列表；缺失表示该命令返回错误。
	cookiesByMethod map[string][]cdpCookie
	// receivedMethods 按到达顺序记录浏览器实际收到的命令名。
	receivedMethods []string
}

// newFakeCDPServer 启动确定性 CDP 测试端点，并在测试结束时关闭。
func newFakeCDPServer(t *testing.T, cookiesByMethod map[string][]cdpCookie) *fakeCDPServer {
	t.Helper()
	// fake 保存本次测试的 CDP 端点状态。
	fake := &fakeCDPServer{cookiesByMethod: cookiesByMethod}
	// mux 承载版本端点和 WebSocket 端点。
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(writer http.ResponseWriter, request *http.Request) {
		// webSocketURL 是本机 WebSocket 端点地址，方案由测试服务器实际协议决定。
		webSocketURL := "ws" + strings.TrimPrefix(fake.server.URL, "http") + "/devtools/browser"
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"webSocketDebuggerUrl": webSocketURL})
	})
	mux.HandleFunc("/devtools/browser", func(writer http.ResponseWriter, request *http.Request) {
		// conn 是本次 CDP 会话连接；关闭由处理器负责。
		conn, acceptErr := websocket.Accept(writer, request, nil)
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
		for {
			// data 是浏览器收到的原始 CDP 命令消息。
			_, data, readErr := conn.Read(request.Context())
			if readErr != nil {
				return
			}
			// envelope 是只保留命令名和关联 id 的最小请求结构。
			var envelope struct {
				// ID 是命令关联标识，必须原样回填到响应。
				ID int64 `json:"id"`
				// Method 是 CDP 命令名。
				Method string `json:"method"`
			}
			// unmarshalErr 是命令解码失败的错误；无法解析的消息直接终止本次端点会话。
			if unmarshalErr := json.Unmarshal(data, &envelope); unmarshalErr != nil {
				return
			}
			fake.receivedMethods = append(fake.receivedMethods, envelope.Method)
			// cookies、supported 表示预设结果；不支持的命令返回 CDP 错误。
			cookies, supported := fake.cookiesByMethod[envelope.Method]
			// response 是回写给客户端的 CDP 响应信封。
			response := map[string]any{"id": envelope.ID}
			if supported {
				response["result"] = map[string]any{"cookies": cookies}
			} else {
				response["error"] = map[string]any{"message": "method not supported"}
			}
			// payload 是序列化后的响应消息。
			payload, marshalErr := json.Marshal(response)
			if marshalErr != nil {
				return
			}
			// writeErr 是回写响应失败的错误，通常表示客户端已提前断开连接。
			if writeErr := conn.Write(request.Context(), websocket.MessageText, payload); writeErr != nil {
				return
			}
		}
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

// port 返回测试服务器监听端口，供 CDP 客户端通过回环地址连接。
func (f *fakeCDPServer) port(t *testing.T) int {
	t.Helper()
	// address 是 host:port 形式的本机监听地址，端口由测试服务器动态分配。
	address := f.server.Listener.Addr().String()
	// separatorIndex 是地址中最后一个冒号位置，用于切出端口。
	separatorIndex := strings.LastIndex(address, ":")
	if separatorIndex < 0 {
		t.Fatalf("测试服务器地址缺少端口: %s", address)
	}
	// port、parseErr 是解析出的监听端口及其失败原因。
	port, parseErr := strconv.Atoi(address[separatorIndex+1:])
	if parseErr != nil {
		t.Fatalf("解析测试服务器端口失败: %v", parseErr)
	}
	return port
}

// TestCDPCookieClientReadsCookiesFromBrowserEndpoint 验证 CDP 客户端通过回环调试端口读取 Cookie。
func TestCDPCookieClientReadsCookiesFromBrowserEndpoint(t *testing.T) {
	// fake 提供支持 Storage.getCookies 的确定性浏览器端点。
	fake := newFakeCDPServer(t, map[string][]cdpCookie{
		"Storage.getCookies": {{Name: "x5sec", Value: "fresh-value", Domain: ".goofish.com"}},
	})
	// client 是本次被测的 CDP Cookie 客户端。
	client := cdpWebSocketCookieClient{client: fake.server.Client()}
	// cookies、err 保存读取结果及失败原因。
	cookies, err := client.Cookies(context.Background(), fake.port(t))
	if err != nil {
		t.Fatalf("读取 CDP Cookie 失败: %v", err)
	}
	if len(cookies) != 1 || cookies[0].Name != "x5sec" || cookies[0].Value != "fresh-value" {
		t.Fatalf("CDP Cookie 结果=%+v", cookies)
	}
	if len(fake.receivedMethods) == 0 || fake.receivedMethods[0] != "Storage.getCookies" {
		t.Fatalf("首选命令应为 Storage.getCookies，实际=%v", fake.receivedMethods)
	}
}

// TestCDPCookieClientFallsBackToLegacyMethod 验证首选命令不可用时回退到 Network.getAllCookies。
func TestCDPCookieClientFallsBackToLegacyMethod(t *testing.T) {
	// fake 只支持旧版 Network.getAllCookies 命令。
	fake := newFakeCDPServer(t, map[string][]cdpCookie{
		"Network.getAllCookies": {{Name: "x5sec", Value: "legacy-value", Domain: ".goofish.com"}},
	})
	// client 是本次被测的 CDP Cookie 客户端。
	client := cdpWebSocketCookieClient{client: fake.server.Client()}
	// cookies、err 保存回退后的读取结果及失败原因。
	cookies, err := client.Cookies(context.Background(), fake.port(t))
	if err != nil {
		t.Fatalf("回退读取 CDP Cookie 失败: %v", err)
	}
	if len(cookies) != 1 || cookies[0].Value != "legacy-value" {
		t.Fatalf("回退 CDP Cookie 结果=%+v", cookies)
	}
	if len(fake.receivedMethods) < 2 || fake.receivedMethods[1] != "Network.getAllCookies" {
		t.Fatalf("应回退到 Network.getAllCookies，实际=%v", fake.receivedMethods)
	}
}

// TestCDPCookieClientRejectsUnavailablePort 验证调试端口不可用时返回明确错误而不阻塞。
func TestCDPCookieClientRejectsUnavailablePort(t *testing.T) {
	// fake 提供一个已关闭的端点地址，用于得到必然失败的调试端口。
	fake := newFakeCDPServer(t, nil)
	// port 是被关闭端点的历史端口，连接应当失败。
	port := fake.port(t)
	fake.server.Close()
	// client 使用短超时客户端，避免测试等待真实网络超时。
	client := cdpWebSocketCookieClient{client: &http.Client{Timeout: 200 * time.Millisecond}}
	// err 表示探测已关闭端口的失败原因；必须非空且不包含凭证。
	if _, err := client.Cookies(context.Background(), port); err == nil {
		t.Fatal("调试端口不可用时必须返回错误")
	}
}

// TestCDPCookieClientRejectsInvalidPort 验证非法端口在发起网络请求前即被拒绝。
func TestCDPCookieClientRejectsInvalidPort(t *testing.T) {
	// client 不需要可用端点，非法端口必须在探测前被拒绝。
	client := cdpWebSocketCookieClient{}
	// port 是当前遍历的非法端口取值。
	for _, port := range []int{0, -1, 70000} {
		// err 是非法端口调用返回的错误；必须在发起网络探测前产生，接口不接受超范围端口。
		if _, err := client.Cookies(context.Background(), port); err == nil {
			t.Fatalf("非法端口 %d 必须返回错误", port)
		}
	}
}

// TestFreshX5secValueRequiresUnseenNonEmptyValue 验证只有全新的非空 x5sec 才算人工验证成功。
func TestFreshX5secValueRequiresUnseenNonEmptyValue(t *testing.T) {
	// cookies 保存包含旧值、空值和全新值的浏览器 Cookie。
	cookies := []cdpCookie{
		{Name: "x5sec", Value: "old-value", Domain: ".goofish.com"},
		{Name: "x5sec", Value: "   ", Domain: ".goofish.com"},
		{Name: "x5sec", Value: "new-value", Domain: ".goofish.com"},
		{Name: "unb", Value: "ignored", Domain: ".goofish.com"},
	}
	// value、ok 是筛选结果；必须返回全新的非空值。
	value, ok := freshX5secValue(cookies, []string{"old-value"})
	if !ok || value != "new-value" {
		t.Fatalf("全新 x5sec 判定=(%q,%v)", value, ok)
	}
}

// TestFreshX5secValueRejectsOnlyKnownOrEmptyValues 验证只有旧值和空值时判定为未完成验证。
func TestFreshX5secValueRejectsOnlyKnownOrEmptyValues(t *testing.T) {
	// onlyOld 是只包含已持有值的 Cookie 集合。
	onlyOld := []cdpCookie{{Name: "x5sec", Value: "same-value", Domain: ".goofish.com"}}
	// value、ok 是判定结果：ok 为 false 表示基线中的旧值不得被当作本次验证产生的新值。
	if value, ok := freshX5secValue(onlyOld, []string{"same-value"}); ok {
		t.Fatalf("旧值不得判定为成功: %q", value)
	}
	// onlyEmpty 是只包含空值的 Cookie 集合。
	onlyEmpty := []cdpCookie{{Name: "x5sec", Value: "", Domain: ".goofish.com"}}
	// value、ok 是判定结果：空值即使不在已知集合中也不算验证成功。
	if value, ok := freshX5secValue(onlyEmpty, nil); ok {
		t.Fatalf("空值不得判定为成功: %q", value)
	}
	// unrelated 是不含 x5sec 的 Cookie 集合。
	unrelated := []cdpCookie{{Name: "unb", Value: "value", Domain: ".goofish.com"}}
	// value、ok 是判定结果：Cookie 中完全没有 x5sec 时必须判定为未完成验证。
	if value, ok := freshX5secValue(unrelated, nil); ok {
		t.Fatalf("缺少 x5sec 不得判定为成功: %q", value)
	}
}
