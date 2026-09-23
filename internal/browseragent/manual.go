package browseragent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrVerificationTimeout 表示等待用户在系统浏览器中完成验证超时。
var ErrVerificationTimeout = errors.New("等待人工完成验证超时")

// ErrVerificationCancelled 表示调用方在验证完成前取消了本次人工验证。
var ErrVerificationCancelled = errors.New("人工验证已取消")

// manualVerifyDefaults 是人工验证的默认等待预算；调用方可通过 Options 覆盖。
const (
	defaultReadyTimeout        = 20 * time.Second
	defaultVerificationTimeout = 5 * time.Minute
	defaultPollInterval        = 2 * time.Second
)

// launcher 定义在交互式用户会话中启动系统浏览器的最小能力。
type launcher interface {
	// Launch 使用账号专用 profile 打开验证地址，并返回浏览器回环调试端口。
	Launch(ctx context.Context, profileDir, verificationURL string) (int, error)
}

// ManualVerifyOptions 描述人工验证的等待预算；零值字段使用默认值。
type ManualVerifyOptions struct {
	// ReadyTimeout 是等待浏览器调试端口就绪的上限。
	ReadyTimeout time.Duration
	// VerificationTimeout 是等待用户完成验证的上限。
	VerificationTimeout time.Duration
	// PollInterval 是两次读取浏览器 Cookie 之间的间隔。
	PollInterval time.Duration
}

// normalized 返回补齐默认值后的等待预算。
func (o ManualVerifyOptions) normalized() ManualVerifyOptions {
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = defaultReadyTimeout
	}
	if o.VerificationTimeout <= 0 {
		o.VerificationTimeout = defaultVerificationTimeout
	}
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPollInterval
	}
	return o
}

// ManualAgent 编排“启动系统浏览器 → 用户手动完成验证 → 读取新 x5sec”的最小闭环。
// 它不执行任何自动滑块交互；成功判定仅依据浏览器内出现的全新非空 x5sec。
type ManualAgent struct {
	// launcher 负责在用户会话中启动浏览器；为 nil 时 Verify 返回不可用。
	launcher launcher
	// cookies 负责按调试端口读取浏览器 Cookie；为 nil 时 Verify 返回不可用。
	cookies cdpCookieClient
	// options 保存等待预算；零值字段在调用时补齐默认值。
	options ManualVerifyOptions
	// sleep 提供可注入的等待函数，Context 取消必须立刻返回。
	sleep func(ctx context.Context, d time.Duration) error
	// now 提供可注入的时钟，用于确定性测试超时行为。
	now func() time.Time
}

// NewManualAgent 构造人工验证代理；launcher 与 cookies 均为必需依赖。
func NewManualAgent(browserLauncher launcher, cookieClient cdpCookieClient, options ManualVerifyOptions) *ManualAgent {
	return &ManualAgent{
		launcher: browserLauncher,
		cookies:  cookieClient,
		options:  options.normalized(),
		sleep:    sleepWithContext,
		now:      time.Now,
	}
}

// Verify 在用户会话中打开验证页面并等待人工完成；返回值只包含验证结论和新 x5sec。
func (a *ManualAgent) Verify(ctx context.Context, request Request) (Result, error) {
	if a == nil || a.launcher == nil || a.cookies == nil {
		return Result{}, ErrUnavailable
	}
	// err 是请求边界校验失败的错误（账号、profile 路径或验证地址不合法），此时不启动浏览器。
	if err := ValidateRequest(request); err != nil {
		return Result{}, err
	}
	// err 是调用方 Context 的取消或超时原因；非空时无需启动浏览器，直接按“已取消”返回。
	if err := ctx.Err(); err != nil {
		return Result{}, ErrVerificationCancelled
	}
	// port、launchErr 是系统浏览器回环调试端口及其启动失败原因。
	port, launchErr := a.launcher.Launch(ctx, request.ProfileDir, request.VerificationURL)
	if launchErr != nil {
		return Result{}, launchErr
	}
	// err 是调试端口在 ReadyTimeout 内未就绪的错误，包含等待超时与取消两种情形。
	if err := a.waitForDebugPort(ctx, port); err != nil {
		return Result{}, err
	}
	// baseline 是判定“全新 x5sec”的基线：账号已持有的值与页面打开时的浏览器现值。
	baseline, baselineErr := a.baselineX5sec(ctx, port, request.KnownX5sec)
	if baselineErr != nil {
		return Result{}, baselineErr
	}
	return a.waitForVerification(ctx, port, baseline)
}

// waitForDebugPort 在 ReadyTimeout 内轮询调试端口，直到浏览器可被 CDP 连接。
func (a *ManualAgent) waitForDebugPort(ctx context.Context, port int) error {
	// deadline 是等待调试端口就绪的最后时刻。
	deadline := a.now().Add(a.options.ReadyTimeout)
	for {
		// err 是调用方 Context 的取消原因；轮询期间取消必须立刻停止等待并回传“已取消”。
		if err := ctx.Err(); err != nil {
			return ErrVerificationCancelled
		}
		// probeErr 为 nil 表示调试端口已经可以响应 CDP 读取。
		if _, probeErr := a.cookies.Cookies(ctx, port); probeErr == nil {
			return nil
		}
		if !a.now().Before(deadline) {
			return fmt.Errorf("系统浏览器调试端口在 %s 内未就绪", a.options.ReadyTimeout)
		}
		// err 是两次端口探测之间等待被取消的错误，非空表示调用方主动放弃本次验证。
		if err := a.sleep(ctx, a.options.PollInterval); err != nil {
			return ErrVerificationCancelled
		}
	}
}

// baselineX5sec 汇总账号已持有的 x5sec 与页面打开时浏览器内的现值，作为成功判定基线。
func (a *ManualAgent) baselineX5sec(ctx context.Context, port int, known []string) ([]string, error) {
	// baseline 预置调用方提供的账号现值，避免把旧值误判为新值。
	baseline := make([]string, 0, len(known)+1)
	// value 是当前遍历到的调用方提供的 x5sec 值。
	for _, value := range known {
		// trimmed 是当前已知值去除首尾空白后的形式；空串属于占位值，不进入基线以免污染判定。
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			baseline = append(baseline, trimmed)
		}
	}
	// cookies、err 是页面打开时浏览器内的 Cookie 现值。
	cookies, err := a.cookies.Cookies(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("读取系统浏览器初始 Cookie 失败: %w", err)
	}
	// cookie 是当前待收集的浏览器 Cookie。
	for _, cookie := range cookies {
		if !strings.EqualFold(strings.TrimSpace(cookie.Name), "x5sec") {
			continue
		}
		// trimmed 是当前 Cookie 值去除首尾空白后的形式；空值不计入基线。
		if trimmed := strings.TrimSpace(cookie.Value); trimmed != "" {
			baseline = append(baseline, trimmed)
		}
	}
	return baseline, nil
}

// waitForVerification 轮询浏览器 Cookie，直到用户完成验证并出现全新的非空 x5sec。
func (a *ManualAgent) waitForVerification(ctx context.Context, port int, baseline []string) (Result, error) {
	// deadline 是等待用户完成验证的最后时刻。
	deadline := a.now().Add(a.options.VerificationTimeout)
	for {
		// err 是调用方 Context 的取消原因；用户等待期间取消必须立即返回，避免遗留浏览器会话。
		if err := ctx.Err(); err != nil {
			return Result{}, ErrVerificationCancelled
		}
		// cookies、err 是本次轮询读取到的浏览器 Cookie。
		cookies, err := a.cookies.Cookies(ctx, port)
		if err == nil {
			// value、ok 是本次轮询发现的全新非空 x5sec 及其是否存在；ok 为 true 才足以判定验证成功。
			if value, ok := freshX5secValue(cookies, baseline); ok {
				return Result{Verified: true, X5sec: value}, nil
			}
		}
		if !a.now().Before(deadline) {
			return Result{}, fmt.Errorf("%w（%s）", ErrVerificationTimeout, a.options.VerificationTimeout)
		}
		// err 是两次 Cookie 轮询之间等待被取消的错误，非空表示调用方放弃了本次等待。
		if err := a.sleep(ctx, a.options.PollInterval); err != nil {
			return Result{}, ErrVerificationCancelled
		}
	}
}

// sleepWithContext 执行可取消等待；Context 取消时立即返回取消错误。
func sleepWithContext(ctx context.Context, d time.Duration) error {
	// timer 是本次等待的定时器，必须在返回前停止以释放资源。
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
