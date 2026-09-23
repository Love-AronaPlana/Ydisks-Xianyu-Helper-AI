package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xianyu-go/internal/browser"
	"xianyu-go/internal/browseragent"
	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/cookierefresh"
)

// fakeBrowserAgent 是人工浏览器验证端口替身，记录请求并返回预设结果。
type fakeBrowserAgent struct {
	// result 是预设的人工验证结论。
	result browseragent.Result
	// err 是预设的验证失败原因。
	err error
	// requests 保存收到的全部人工验证请求，用于断言账号隔离与基线传递。
	requests []browseragent.Request
}

// Verify 实现 browseragent.BrowserAgent 端口并记录请求。
func (f *fakeBrowserAgent) Verify(_ context.Context, request browseragent.Request) (browseragent.Result, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return browseragent.Result{}, f.err
	}
	return f.result, nil
}

// TestManualTokenCaptchaVerifierMergesFreshX5sec 验证人工验证成功后只把新 x5sec 合并回原 Cookie。
func TestManualTokenCaptchaVerifierMergesFreshX5sec(t *testing.T) {
	// agent 返回用户手动完成验证后的新 x5sec。
	agent := &fakeBrowserAgent{result: browseragent.Result{Verified: true, X5sec: "fresh-value"}}
	// verifier 是本次被测的人工验证适配器。
	verifier := NewManualTokenCaptchaVerifier(agent)
	// cookies、err 保存合并后的 Cookie 字符串及其失败原因。
	cookies, err := verifier.VerifyTokenCaptcha(context.Background(), TokenCaptchaManualVerificationRequest{
		CookieID: "account-1", CookieStr: "unb=123; x5sec=old-value; cna=abc", VerificationURL: "https://example.com/verify",
	})
	if err != nil {
		t.Fatalf("人工验证适配器失败: %v", err)
	}
	// merged 是合并结果解析出的键值表。
	merged := cookierefresh.ParseCookieString(cookies)
	if merged["x5sec"] != "fresh-value" {
		t.Fatalf("x5sec 未合并: %q", cookies)
	}
	if merged["unb"] != "123" || merged["cna"] != "abc" {
		t.Fatalf("其他 Cookie 字段被破坏: %q", cookies)
	}
	// 账号已持有的 x5sec 必须作为成功判定基线传给代理，避免旧值被当成新值。
	if len(agent.requests) != 1 || len(agent.requests[0].KnownX5sec) != 1 || agent.requests[0].KnownX5sec[0] != "old-value" {
		t.Fatalf("人工验证基线=%+v", agent.requests)
	}
	if agent.requests[0].AccountID != "account-1" || agent.requests[0].VerificationURL != "https://example.com/verify" {
		t.Fatalf("人工验证请求=%+v", agent.requests[0])
	}
}

// TestManualTokenCaptchaVerifierRejectsMissingAgent 验证未配置代理时明确返回不可用而不伪造成功。
func TestManualTokenCaptchaVerifierRejectsMissingAgent(t *testing.T) {
	// verifier 没有注入任何代理，必须返回不可用。
	verifier := NewManualTokenCaptchaVerifier(nil)
	// err 必须是不可用错误，且 Cookie 结果为空。
	_, err := verifier.VerifyTokenCaptcha(context.Background(), TokenCaptchaManualVerificationRequest{
		CookieID: "account-1", CookieStr: "x5sec=old", VerificationURL: "https://example.com/verify",
	})
	if !errors.Is(err, browseragent.ErrUnavailable) {
		t.Fatalf("缺少代理应返回 ErrUnavailable，实际 err=%v", err)
	}
}

// TestManualTokenCaptchaVerifierRejectsIncompleteVerification 验证代理未确认完成时不返回新 Cookie。
func TestManualTokenCaptchaVerifierRejectsIncompleteVerification(t *testing.T) {
	// case 是当前待检查的不完整验证结果。
	for _, testCase := range []struct {
		// name 是该用例的测试名称。
		name string
		// result 是代理返回的不完整结果。
		result browseragent.Result
	}{
		{name: "not-verified", result: browseragent.Result{Verified: false, X5sec: "value"}},
		{name: "empty-x5sec", result: browseragent.Result{Verified: true, X5sec: "   "}},
	} {
		// verifier 是被测验证器，其代理固定返回当前不完整结果。
		verifier := NewManualTokenCaptchaVerifier(&fakeBrowserAgent{result: testCase.result})
		// cookies、err 必须表示人工验证尚未产生可用的新值。
		cookies, err := verifier.VerifyTokenCaptcha(context.Background(), TokenCaptchaManualVerificationRequest{
			CookieID: "account-1", CookieStr: "x5sec=old", VerificationURL: "https://example.com/verify",
		})
		if err == nil || cookies != "" {
			t.Fatalf("%s cookies=%q err=%v", testCase.name, cookies, err)
		}
	}
}

// TestManualTokenCaptchaVerifierValidatesRequest 验证缺少验证地址或账号时在启动浏览器前即失败。
func TestManualTokenCaptchaVerifierValidatesRequest(t *testing.T) {
	// agent 记录不应发生的浏览器启动请求。
	agent := &fakeBrowserAgent{}
	// verifier 是被测验证器，用于断言请求校验发生在浏览器启动之前。
	verifier := NewManualTokenCaptchaVerifier(agent)
	// case 是当前待检查的非法请求。
	for _, testCase := range []struct {
		// name 是该用例的测试名称。
		name string
		// request 是缺少必要字段的人工验证请求。
		request TokenCaptchaManualVerificationRequest
	}{
		{name: "missing-url", request: TokenCaptchaManualVerificationRequest{CookieID: "account-1", CookieStr: "x5sec=old"}},
		{name: "missing-account", request: TokenCaptchaManualVerificationRequest{CookieStr: "x5sec=old", VerificationURL: "https://example.com/verify"}},
	} {
		// err 是非法请求返回的校验错误，必须可被识别为 browseragent.ErrInvalidRequest。
		if _, err := verifier.VerifyTokenCaptcha(context.Background(), testCase.request); !errors.Is(err, browseragent.ErrInvalidRequest) {
			t.Fatalf("%s err=%v, want ErrInvalidRequest", testCase.name, err)
		}
	}
	if len(agent.requests) != 0 {
		t.Fatalf("非法请求不得启动浏览器，实际次数=%d", len(agent.requests))
	}
}

// TestNormalizeCaptchaBrowserModeKeepsKnownModesAndFallsBack 验证模式归一化只接受受支持枚举。
func TestNormalizeCaptchaBrowserModeKeepsKnownModesAndFallsBack(t *testing.T) {
	// got 是归一化后的浏览器模式，受支持枚举必须原样保留。
	if got := normalizeCaptchaBrowserMode("system_manual"); got != db.CaptchaBrowserModeSystemManual {
		t.Fatalf("system_manual 归一化=%q", got)
	}
	// got 是去除空白并统一大小写后的模式，仍须映射到系统人工验证模式。
	if got := normalizeCaptchaBrowserMode(" System_Manual "); got != db.CaptchaBrowserModeSystemManual {
		t.Fatalf("大小写与空白应被归一化，实际=%q", got)
	}
	// unknown 表示历史或非法取值，必须回落自动模式。
	for _, unknown := range []string{"", "playwright", "remote", "unknown"} {
		// got 是非法取值的归一化结果，必须回落自动模式而不是保留未知值。
		if got := normalizeCaptchaBrowserMode(unknown); got != db.CaptchaBrowserModePlaywright {
			t.Fatalf("非法模式 %q 归一化=%q", unknown, got)
		}
	}
}

// TestKnownX5secValuesReadsOnlyNonEmptyValue 验证基线只包含非空的 x5sec 现值。
func TestKnownX5secValuesReadsOnlyNonEmptyValue(t *testing.T) {
	// values 是提取出的已知 x5sec 集合，非空现值必须被完整收集。
	if values := knownX5secValues("unb=1; x5sec=value; cna=2"); len(values) != 1 || values[0] != "value" {
		t.Fatalf("已知 x5sec=%v", values)
	}
	// values 是提取出的已知 x5sec 集合，空值不得计入基线以免成功判定失效。
	if values := knownX5secValues("unb=1; x5sec=; cna=2"); len(values) != 0 {
		t.Fatalf("空 x5sec 不应计入基线: %v", values)
	}
	// values 是空 Cookie 字符串的提取结果，必须是空集合而不是错误。
	if values := knownX5secValues(""); len(values) != 0 {
		t.Fatalf("空 Cookie 不应产生基线: %v", values)
	}
}

// TestManualVerificationProfileIsIsolatedPerAccount 验证人工验证 profile 目录按账号隔离且不与自动化 profile 共用。
func TestManualVerificationProfileIsIsolatedPerAccount(t *testing.T) {
	// 该用例只验证 profile 路径解析，不接触真实浏览器。
	first, firstErr := browser.ManualVerificationUserDataDir("account-a")
	if firstErr != nil {
		t.Fatalf("解析人工验证 profile 失败: %v", firstErr)
	}
	// second、secondErr 是另一账号的人工验证 profile 目录及其解析错误，用于验证账号间隔离。
	second, secondErr := browser.ManualVerificationUserDataDir("account-b")
	if secondErr != nil {
		t.Fatalf("解析人工验证 profile 失败: %v", secondErr)
	}
	if first == second {
		t.Fatalf("不同账号的人工验证 profile 必须隔离: %q", first)
	}
	if !strings.Contains(first, "manual_") {
		t.Fatalf("人工验证 profile 应位于独立目录: %q", first)
	}
}
