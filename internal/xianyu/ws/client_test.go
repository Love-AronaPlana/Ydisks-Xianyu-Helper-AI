package ws

import (
	"encoding/base64"
	"testing"

	"xianyu-go/internal/xianyu"
)

// TestWebsocketHeadersMatchBrowserHandshake 封装TestWebsocketHeadersMatch浏览器Handshake业务协调。
func TestWebsocketHeadersMatchBrowserHandshake(t *testing.T) {
	xianyu.SetBrowserFingerprint(xianyu.BrowserFingerprint{UserAgent: "runtime-browser-ua"})
	// got 用于本次流程后续判断的got
	got := websocketHeaders()
	if got.Get("Origin") != "https://www.goofish.com" || got.Get("User-Agent") != "runtime-browser-ua" {
		t.Fatalf("websocket headers = %#v", got)
	}
	if got.Get("Cookie") != "" {
		t.Fatalf("dingtalk WebSocket 不应收到 goofish Cookie: %#v", got)
	}
}

// TestOfficialRegistrationUAUsesRuntimeBrowserVersion 封装TestOfficialRegistrationUAUsesRuntime浏览器Version业务协调。
func TestOfficialRegistrationUAUsesRuntimeBrowserVersion(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/138.0.7204.92 Safari/537.36"
	// want 用于本次流程后续判断的want
	want := raw + " DingTalk(2.2.0) OS(Mac OS/10.15.7) Browser(Chrome/138.0.7204.92) DingWeb/2.2.0 IMPaaS DingWeb/2.2.0"
	if // got 用于本次流程后续判断的got
	got := OfficialRegistrationUA(raw); got != want {
		t.Fatalf("OfficialRegistrationUA() = %q, want %q", got, want)
	}
}

// TestOfficialRegistrationUARecognizesHeadlessChrome 封装TestOfficialRegistrationUARecognizesHeadlessChrome业务协调。
func TestOfficialRegistrationUARecognizesHeadlessChrome(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 HeadlessChrome/138.0.7204.92 Safari/537.36"
	// want 用于本次流程后续判断的want
	want := raw + " DingTalk(2.2.0) OS(Linux/other) Browser(Chrome Headless/138.0.7204.92) DingWeb/2.2.0 IMPaaS DingWeb/2.2.0"
	if // got 用于本次流程后续判断的got
	got := OfficialRegistrationUA(raw); got != want {
		t.Fatalf("OfficialRegistrationUA() = %q, want %q", got, want)
	}
}

// TestExtractSyncPayloads 验证同一同步帧内的全部条目都会被提取：三条目夹具按平台顺序返回
// 下标 0/1 的有效条目（first、second）与下标 2 的有效性失败条目（data 为非字符串 1），
// 非对象条目单独标记失败，空帧与畸形帧返回 ok=false。
func TestExtractSyncPayloads(t *testing.T) {
	// msg 是本用例的同步推送帧；第三个条目的 data 为数字 1，用于触发"缺少非空字符串 data"分支。
	msg := map[string]any{"body": map[string]any{"syncPushPackage": map[string]any{
		"data": []any{
			map[string]any{"data": "first"},
			map[string]any{"data": "second"},
			map[string]any{"data": 1},
		},
	}}}
	// entries、ok 是逐条提取结果与整帧可处理标志。
	entries, ok := extractSyncPayloads(msg)
	if !ok {
		t.Fatal("合法同步帧应返回 ok=true")
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d，期望 3", len(entries))
	}
	if entries[0].index != 0 || !entries[0].valid || entries[0].data != "first" {
		t.Fatalf("第 0 条目 = %#v", entries[0])
	}
	if entries[1].index != 1 || !entries[1].valid || entries[1].data != "second" {
		t.Fatalf("第 1 条目 = %#v", entries[1])
	}
	if entries[2].index != 2 || entries[2].valid || entries[2].invalidReason != "缺少非空字符串 data" {
		t.Fatalf("第 2 条目 = %#v", entries[2])
	}
	// mixedEntries、mixedOK 是非对象条目与有效条目混排时的提取结果；非对象条目不得影响后续条目。
	mixedEntries, mixedOK := extractSyncPayloads(map[string]any{"body": map[string]any{"syncPushPackage": map[string]any{
		"data": []any{"raw-not-object", map[string]any{"data": "kept"}},
	}}})
	if !mixedOK || len(mixedEntries) != 2 {
		t.Fatalf("混合条目提取结果 = %#v, ok=%v", mixedEntries, mixedOK)
	}
	if mixedEntries[0].valid || mixedEntries[0].invalidReason != "条目不是对象" {
		t.Fatalf("非对象条目 = %#v", mixedEntries[0])
	}
	if mixedEntries[1].index != 1 || !mixedEntries[1].valid || mixedEntries[1].data != "kept" {
		t.Fatalf("非对象条目之后的条目 = %#v", mixedEntries[1])
	}
	// invalid 是当前待验证的非同步或畸形帧样本，均不得被当作同步推送包。
	for _, invalid := range []map[string]any{{}, {"body": map[string]any{}}, {"body": map[string]any{"syncPushPackage": map[string]any{"data": []any{}}}}} {
		if // invalidOK 表示当前畸形帧是否被误判为可处理的同步推送。
		_, invalidOK := extractSyncPayloads(invalid); invalidOK {
			t.Fatalf("invalid payload accepted: %#v", invalid)
		}
	}
}

// TestDecodeSyncDataJSONAndInvalid 封装TestDecodeSync数据JSONAndInvalid业务协调。
func TestDecodeSyncDataJSONAndInvalid(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := base64.StdEncoding.EncodeToString([]byte(`{"event":"paid","count":2}`))
	// got、err 用于本次流程后续判断的got、err
	got, err := decodeSyncData(raw)
	if err != nil || got["event"] != "paid" || got["count"] != float64(2) {
		t.Fatalf("decodeSyncData() = %#v, %v", got, err)
	}
	if // err 用于本次流程后续判断的err
	_, err := decodeSyncData("not-base64"); err == nil {
		t.Fatal("invalid payload should fail")
	}
}

// TestWSHelpers 封装TestWSHelpers业务协调。
func TestWSHelpers(t *testing.T) {
	if // got 用于本次流程后续判断的got
	got := stripGoofish(" 123@goofish "); got != "123" {
		t.Fatalf("stripGoofish = %q", got)
	}
}
