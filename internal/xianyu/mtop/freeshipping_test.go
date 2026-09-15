package mtop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestFreeShippingContextSendsPlatformPayload 验证免拼发货请求使用独立 MTOP API，且订单、商品和买家标识保持平台要求的 JSON 类型。
func TestFreeShippingContextSendsPlatformPayload(t *testing.T) {
	// server 是检查免拼 MTOP 查询、表单和 Cookie 作用域的本地平台替身。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Query().Get("api") != "mtop.idle.groupon.activity.seller.freeshipping" || request.URL.Query().Get("type") != "originaljson" {
			t.Fatalf("免拼请求元信息错误: method=%s query=%v", request.Method, request.URL.Query())
		}
		if !strings.Contains(request.Header.Get("Cookie"), "_m_h5_tk=token_1") {
			t.Fatalf("免拼请求未携带签名 Cookie: %q", request.Header.Get("Cookie"))
		}
		// parseErr 保存表单解析失败，失败时无法继续验证签名 JSON 负载。
		if parseErr := request.ParseForm(); parseErr != nil {
			t.Fatalf("解析免拼表单失败: %v", parseErr)
		}
		// payload 保存免拼接口接收到的业务标识；数字字段必须保持为 JSON number。
		var payload struct {
			OrderID string `json:"bizOrderId"`
			ItemID  uint64 `json:"itemId"`
			BuyerID uint64 `json:"buyerId"`
		}
		// decodeErr 保存 data 字段 JSON 解码失败。
		if decodeErr := json.Unmarshal([]byte(request.FormValue("data")), &payload); decodeErr != nil {
			t.Fatalf("解析免拼 data 失败: %v", decodeErr)
		}
		if payload.OrderID != "order-1" || payload.ItemID != 10001 || payload.BuyerID != 20002 {
			t.Fatalf("免拼业务负载错误: %+v", payload)
		}
		_, _ = fmt.Fprint(writer, `{"ret":["SUCCESS::调用成功"]}`)
	}))
	defer server.Close()
	// client 是注入本地端点的 MTOP 客户端，避免测试访问真实平台。
	client := &ClientImpl{HTTPClient: server.Client(), FreeShippingURL: server.URL + "/"}
	// ok、ret、updated、callErr 保存免拼请求返回的业务结果与 Cookie 结果。
	ok, ret, updated, callErr := client.FreeShippingContext(context.Background(), consignCookies, "order-1", "10001", "20002")
	if callErr != nil || !ok || len(ret) != 1 || updated != consignCookies {
		t.Fatalf("免拼结果异常: ok=%v ret=%v updated=%q err=%v", ok, ret, updated, callErr)
	}
}

// TestFreeShippingContextRetriesWithRotatedToken 验证免拼端点在平台下发新签名 Cookie 后使用该 Cookie 重试原业务请求。
func TestFreeShippingContextRetriesWithRotatedToken(t *testing.T) {
	// requests 统计平台替身看到的免拼请求次数，首次 Token 过期后应只重试一次。
	var requests atomic.Int32
	// server 在首次请求下发新 Token 并返回过期，在第二次请求确认 Cookie 已轮换后成功。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// attempt 是当前免拼请求的从一开始的序号。
		attempt := requests.Add(1)
		if attempt == 1 {
			http.SetCookie(writer, &http.Cookie{Name: "_m_h5_tk", Value: "fresh_2", Path: "/"})
			_, _ = fmt.Fprint(writer, `{"ret":["FAIL_SYS_TOKEN_EXOIRED::令牌过期"]}`)
			return
		}
		if !strings.Contains(request.Header.Get("Cookie"), "_m_h5_tk=fresh_2") {
			t.Fatalf("重试未使用平台下发的新 Token: %q", request.Header.Get("Cookie"))
		}
		_, _ = fmt.Fprint(writer, `{"ret":["SUCCESS::调用成功"]}`)
	}))
	defer server.Close()
	// client 是把免拼请求导向本地替身的 MTOP 客户端。
	client := &ClientImpl{HTTPClient: server.Client(), FreeShippingURL: server.URL + "/"}
	// ok、ret、updated、callErr 保存 Token 轮换重试后的最终业务结果。
	ok, ret, updated, callErr := client.FreeShippingContext(context.Background(), consignCookies, "order-2", "10001", "20002")
	if callErr != nil || !ok || len(ret) != 1 || requests.Load() != 2 || !strings.Contains(updated, "_m_h5_tk=fresh_2") {
		t.Fatalf("免拼 Token 重试异常: ok=%v ret=%v updated=%q requests=%d err=%v", ok, ret, updated, requests.Load(), callErr)
	}
}

// TestFreeShippingContextRejectsNonNumericActivityIdentifiers 验证免拼端点拒绝非数字商品或买家标识，避免把未校验数据送往平台。
func TestFreeShippingContextRejectsNonNumericActivityIdentifiers(t *testing.T) {
	// client 是不应发起 HTTP 请求的零值客户端；参数校验必须在网络调用前完成。
	client := &ClientImpl{}
	// _, _, _, itemErr 保存商品标识非法时的本地参数校验错误。
	_, _, _, itemErr := client.FreeShippingContext(context.Background(), consignCookies, "order-3", "item-invalid", "20002")
	if itemErr == nil || !strings.Contains(itemErr.Error(), "商品ID不是数字") {
		t.Fatalf("商品标识非法错误=%v", itemErr)
	}
	// _, _, _, buyerErr 保存买家标识非法时的本地参数校验错误。
	_, _, _, buyerErr := client.FreeShippingContext(context.Background(), consignCookies, "order-3", "10001", "buyer-invalid")
	if buyerErr == nil || !strings.Contains(buyerErr.Error(), "买家ID不是数字") {
		t.Fatalf("买家标识非法错误=%v", buyerErr)
	}
}
