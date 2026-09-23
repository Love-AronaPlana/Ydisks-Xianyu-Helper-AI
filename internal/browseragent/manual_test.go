package browseragent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeLauncher 记录人工验证浏览器启动请求并返回预设端口或错误。
type fakeLauncher struct {
	// port 是启动成功后返回的回环调试端口。
	port int
	// err 是启动失败原因；非空时直接返回而不进入等待。
	err error
	// calls 记录启动次数，用于确认未重复启动浏览器。
	calls int
	// lastProfileDir 保存最近一次启动使用的账号 profile 目录。
	lastProfileDir string
	// lastVerificationURL 保存最近一次启动打开的验证地址。
	lastVerificationURL string
}

// Launch 实现 launcher 端口并记录调用参数。
func (f *fakeLauncher) Launch(_ context.Context, profileDir, verificationURL string) (int, error) {
	f.calls++
	f.lastProfileDir = profileDir
	f.lastVerificationURL = verificationURL
	if f.err != nil {
		return 0, f.err
	}
	return f.port, nil
}

// fakeCookieClient 按调用次数依次返回预设的 Cookie 批次，用于模拟用户逐步完成验证。
type fakeCookieClient struct {
	// batches 是按调用顺序返回的 Cookie 批次；超出长度后重复最后一批。
	batches [][]cdpCookie
	// errors 是按调用顺序返回的错误；非 nil 项优先于批次结果。
	errors []error
	// calls 记录读取次数，用于断言轮询行为。
	calls int
}

// Cookies 实现 cdpCookieClient 端口并按调用序号返回预设结果。
func (f *fakeCookieClient) Cookies(_ context.Context, _ int) ([]cdpCookie, error) {
	// index 是本次调用的序号，超出预设范围时保持在最后一项。
	index := f.calls
	f.calls++
	if index < len(f.errors) && f.errors[index] != nil {
		return nil, f.errors[index]
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	if index >= len(f.batches) {
		index = len(f.batches) - 1
	}
	return f.batches[index], nil
}

// newManualTestAgent 构造使用假时钟的确定性人工验证代理。
func newManualTestAgent(browserLauncher launcher, cookies cdpCookieClient, options ManualVerifyOptions) (*ManualAgent, *time.Time) {
	// agent 是被测人工验证代理；随后替换其时钟与等待函数以得到确定性超时行为。
	agent := NewManualAgent(browserLauncher, cookies, options)
	// clock 是测试共享的可推进时钟，避免真实等待。
	clock := time.Unix(1_700_000_000, 0).UTC()
	agent.now = func() time.Time { return clock }
	agent.sleep = func(_ context.Context, d time.Duration) error {
		clock = clock.Add(d)
		return nil
	}
	return agent, &clock
}

// manualRequest 返回一个通过校验的人工验证请求。
func manualRequest(t *testing.T, known []string) Request {
	t.Helper()
	return Request{
		AccountID:       "account-1",
		ProfileDir:      t.TempDir(),
		VerificationURL: "https://example.com/verify",
		KnownX5sec:      known,
	}
}

// TestManualAgentReturnsFreshX5secAfterUserVerification 验证用户在系统浏览器完成验证后返回全新的 x5sec。
func TestManualAgentReturnsFreshX5secAfterUserVerification(t *testing.T) {
	// cookies 前两次返回尚未出现新值，第三次返回用户验证后的新值。
	cookies := &fakeCookieClient{batches: [][]cdpCookie{
		{}, // 调试端口就绪探测
		{{Name: "x5sec", Value: "old-value", Domain: ".goofish.com"}}, // 基线快照
		{{Name: "x5sec", Value: "old-value", Domain: ".goofish.com"}},
		{{Name: "x5sec", Value: "fresh-value", Domain: ".goofish.com"}},
	}}
	// browserLauncher 返回固定端口，确认浏览器只启动一次。
	browserLauncher := &fakeLauncher{port: 9222}
	// agent 是使用假时钟的被测代理；第二个返回值是共享时钟，本用例不需要推进时钟故忽略。
	agent, _ := newManualTestAgent(browserLauncher, cookies, ManualVerifyOptions{})
	// result、err 是人工验证结论及其失败原因。
	result, err := agent.Verify(context.Background(), manualRequest(t, []string{"old-value"}))
	if err != nil {
		t.Fatalf("人工验证失败: %v", err)
	}
	if !result.Verified || result.X5sec != "fresh-value" {
		t.Fatalf("人工验证结果=%+v", result)
	}
	if browserLauncher.calls != 1 {
		t.Fatalf("浏览器启动次数=%d", browserLauncher.calls)
	}
	if browserLauncher.lastVerificationURL != "https://example.com/verify" {
		t.Fatalf("验证地址=%q", browserLauncher.lastVerificationURL)
	}
}

// TestManualAgentRejectsValueAlreadyKnownByAccount 验证账号已持有的 x5sec 不算人工验证成功。
func TestManualAgentRejectsValueAlreadyKnownByAccount(t *testing.T) {
	// cookies 始终只返回账号已持有的旧值，用户实际上没有产生新验证。
	cookies := &fakeCookieClient{batches: [][]cdpCookie{
		{},
		{{Name: "x5sec", Value: "known-value", Domain: ".goofish.com"}},
	}}
	// agent 是使用假时钟的被测代理，用于观察旧值是否被误判为验证成功。
	agent, _ := newManualTestAgent(&fakeLauncher{port: 9222}, cookies, ManualVerifyOptions{})
	// err 必须是超时，而不是把旧值当作成功。
	_, err := agent.Verify(context.Background(), manualRequest(t, []string{"known-value"}))
	if !errors.Is(err, ErrVerificationTimeout) {
		t.Fatalf("旧值应判定为超时，实际 err=%v", err)
	}
}

// TestManualAgentTimesOutWithoutUserAction 验证用户未操作时按等待上限返回超时错误。
func TestManualAgentTimesOutWithoutUserAction(t *testing.T) {
	// cookies 永远不返回 x5sec，模拟用户一直没有完成验证。
	cookies := &fakeCookieClient{batches: [][]cdpCookie{{}}}
	// agent 是使用假时钟的被测代理；等待预算被压缩到秒级以便确定性触发超时。
	agent, _ := newManualTestAgent(&fakeLauncher{port: 9222}, cookies, ManualVerifyOptions{
		ReadyTimeout:        time.Second,
		VerificationTimeout: 3 * time.Second,
		PollInterval:        time.Second,
	})
	// err 必须是超时错误且不包含任何凭证。
	_, err := agent.Verify(context.Background(), manualRequest(t, nil))
	if !errors.Is(err, ErrVerificationTimeout) {
		t.Fatalf("未完成验证应超时，实际 err=%v", err)
	}
}

// TestManualAgentHonoursContextCancellation 验证调用方取消后立即停止等待。
func TestManualAgentHonoursContextCancellation(t *testing.T) {
	// cookies 永不满足成功条件，取消必须由 Context 触发。
	cookies := &fakeCookieClient{batches: [][]cdpCookie{{}}}
	// agent 是被测代理，使用真实 time.Now 以验证 Context 取消能立即终止等待。
	agent := NewManualAgent(&fakeLauncher{port: 9222}, cookies, ManualVerifyOptions{
		ReadyTimeout:        time.Minute,
		VerificationTimeout: time.Minute,
		PollInterval:        time.Millisecond,
	})
	// ctx 是已取消的调用方上下文。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// err 必须是取消错误而不是超时错误。
	_, err := agent.Verify(ctx, manualRequest(t, nil))
	if !errors.Is(err, ErrVerificationCancelled) {
		t.Fatalf("取消后应返回取消错误，实际 err=%v", err)
	}
}

// TestManualAgentPropagatesLauncherFailure 验证浏览器启动失败时直接返回原因且不进入轮询。
func TestManualAgentPropagatesLauncherFailure(t *testing.T) {
	// launchErr 是预设的浏览器启动失败原因。
	launchErr := errors.New("未找到 Google Chrome 或 Microsoft Edge")
	// browserLauncher 是必定返回失败的启动器；cookies 用于断言启动失败后不会进入轮询。
	browserLauncher := &fakeLauncher{err: launchErr}
	// cookies 记录不应对其发起读取的 Cookie 客户端。
	cookies := &fakeCookieClient{batches: [][]cdpCookie{{}}}
	// agent 是被测代理，用于验证启动失败原因被原样传播。
	agent, _ := newManualTestAgent(browserLauncher, cookies, ManualVerifyOptions{})
	// err 必须保留启动失败原因，且不应发生 Cookie 轮询。
	_, err := agent.Verify(context.Background(), manualRequest(t, nil))
	if !errors.Is(err, launchErr) {
		t.Fatalf("应返回启动失败原因，实际 err=%v", err)
	}
	if cookies.calls != 0 {
		t.Fatalf("启动失败后不应读取 Cookie，实际次数=%d", cookies.calls)
	}
}

// TestManualAgentReportsUnavailableWithoutDependencies 验证缺少依赖时明确返回不可用而不伪造成功。
func TestManualAgentReportsUnavailableWithoutDependencies(t *testing.T) {
	// case 是当前待检查的缺失依赖组合。
	for _, testCase := range []struct {
		// name 是该组合的测试名称。
		name string
		// agent 是被测的人工验证代理。
		agent *ManualAgent
	}{
		{name: "nil-agent", agent: nil},
		{name: "missing-launcher", agent: NewManualAgent(nil, &fakeCookieClient{}, ManualVerifyOptions{})},
		{name: "missing-cookie-client", agent: NewManualAgent(&fakeLauncher{}, nil, ManualVerifyOptions{})},
	} {
		// result、err 是缺失依赖时的验证结果与错误；必须为 ErrUnavailable 且不得携带任何验证数据。
		result, err := testCase.agent.Verify(context.Background(), Request{})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s error=%v, want ErrUnavailable", testCase.name, err)
		}
		if result.Verified || result.X5sec != "" {
			t.Fatalf("%s result=%+v 不得包含验证数据", testCase.name, result)
		}
	}
}

// TestManualAgentRejectsInvalidRequestBeforeLaunching 验证非法请求在启动浏览器前即被拒绝。
func TestManualAgentRejectsInvalidRequestBeforeLaunching(t *testing.T) {
	// browserLauncher 记录是否发生了不应出现的启动调用。
	browserLauncher := &fakeLauncher{port: 9222}
	// agent 是被测代理，用于验证非法请求在启动浏览器前即被拒绝。
	agent, _ := newManualTestAgent(browserLauncher, &fakeCookieClient{}, ManualVerifyOptions{})
	// err 必须是请求校验错误。
	err := func() error {
		// verifyErr 是代理对非法 ProfileDir 请求返回的错误，应被识别为 ErrInvalidRequest。
		_, verifyErr := agent.Verify(context.Background(), Request{AccountID: "account", ProfileDir: "relative", VerificationURL: "file:///secret"})
		return verifyErr
	}()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("非法请求应返回 ErrInvalidRequest，实际 err=%v", err)
	}
	if browserLauncher.calls != 0 {
		t.Fatalf("非法请求不得启动浏览器，实际次数=%d", browserLauncher.calls)
	}
}

// TestWindowsAgentIsAvailableOnWindows 验证 Windows 平台默认代理已接入真实人工验证实现。
func TestWindowsAgentIsAvailableOnWindows(t *testing.T) {
	// agent 是 Windows 平台默认的人工验证代理。
	agent := NewWindowsAgent()
	// 非法请求必须在接触浏览器前被拒绝，说明代理已接通真实实现而非占位失败实现。
	err := func() error {
		// verifyErr 是空请求返回的错误；缺少账号与 profile 时应在接触浏览器前被拒绝。
		_, verifyErr := agent.Verify(context.Background(), Request{})
		return verifyErr
	}()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Windows 代理应校验请求，实际 err=%v", err)
	}
}
