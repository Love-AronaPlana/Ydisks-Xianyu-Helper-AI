package adapter

import (
	"context"
	"fmt"
	"strings"

	"xianyu-go/internal/browser"
	"xianyu-go/internal/browseragent"
	"xianyu-go/internal/xianyu/cookierefresh"
)

// ManualTokenCaptchaVerifier 把系统浏览器人工验证代理适配为 Adapter 的人工验证端口。
// 它只负责启动人工验证、判定新 x5sec 并合并 Cookie，不执行任何自动滑块交互。
type ManualTokenCaptchaVerifier struct {
	// agent 是平台人工验证代理；为 nil 时明确返回不可用，绝不伪造成功。
	agent browseragent.BrowserAgent
}

// NewManualTokenCaptchaVerifier 创建人工验证适配器。
func NewManualTokenCaptchaVerifier(agent browseragent.BrowserAgent) *ManualTokenCaptchaVerifier {
	return &ManualTokenCaptchaVerifier{agent: agent}
}

// VerifyTokenCaptcha 在系统浏览器中等待用户完成验证，并把全新的 x5sec 合并回 Cookie 字符串。
// 返回的空字符串表示验证未完成；调用方必须保留原 Cookie 并回退到明确的人工处理提示。
func (v *ManualTokenCaptchaVerifier) VerifyTokenCaptcha(ctx context.Context, request TokenCaptchaManualVerificationRequest) (string, error) {
	if v == nil || v.agent == nil {
		return "", browseragent.ErrUnavailable
	}
	// verificationURL 是本次需要用户手动完成的验证地址。
	verificationURL := strings.TrimSpace(request.VerificationURL)
	if verificationURL == "" {
		return "", fmt.Errorf("%w: 缺少验证地址", browseragent.ErrInvalidRequest)
	}
	if strings.TrimSpace(request.CookieID) == "" {
		return "", fmt.Errorf("%w: 缺少账号标识", browseragent.ErrInvalidRequest)
	}
	// profileDir 是该账号人工验证专用的独立浏览器目录，避免与自动化 profile 争用同一把锁。
	profileDir, err := browser.ManualVerificationUserDataDir(request.CookieID)
	if err != nil {
		return "", err
	}
	// known 是账号当前已持有的 x5sec 值，用于判定人工验证是否产生了全新值。
	known := knownX5secValues(request.CookieStr)
	// result 是人工验证结论；只有明确 Verified 且带回新值才继续。
	result, err := v.agent.Verify(ctx, browseragent.Request{
		AccountID:       request.CookieID,
		ProfileDir:      profileDir,
		VerificationURL: verificationURL,
		KnownX5sec:      known,
	})
	if err != nil {
		return "", err
	}
	// value 是人工验证产生的新 x5sec；为空表示用户尚未完成验证。
	value := strings.TrimSpace(result.X5sec)
	if !result.Verified || value == "" {
		return "", fmt.Errorf("人工验证未产生新的 x5sec")
	}
	// 只把新 x5sec 合并回原 Cookie，其他字段保持账号当前值不变。
	return cookierefresh.MergeOriginalFields(request.CookieStr, "x5sec="+value), nil
}

// knownX5secValues 从 Cookie 字符串中提取账号当前已持有的全部 x5sec 值。
func knownX5secValues(cookieStr string) []string {
	// parsed 是 Cookie 字符串解析出的键值表。
	parsed := cookierefresh.ParseCookieString(cookieStr)
	// values 保存非空的 x5sec 现值；没有该字段时返回空集合。
	var values []string
	// value 是 Cookie 中 x5sec 字段去除空白后的现值；空串表示账号当前没有有效的 x5sec。
	if value := strings.TrimSpace(parsed["x5sec"]); value != "" {
		values = append(values, value)
	}
	return values
}
