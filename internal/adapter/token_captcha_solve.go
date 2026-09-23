package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"xianyu-go/internal/browser"
	"xianyu-go/internal/db"
)

// tokenCaptchaSolveInput 描述一次 token 风控求解所需的非敏感上下文。
// CookieStr 只在本次求解内短暂使用，调用方不得把任何字段写入日志或错误消息。
type tokenCaptchaSolveInput struct {
	// CookieID 是触发风控的账号标识。
	CookieID string
	// CookieStr 是当前账号的 Cookie 明文，仅供平台请求与浏览器注入使用。
	CookieStr string
	// VerificationURL 是平台给出的待验证地址。
	VerificationURL string
	// DeviceID 是平台约定的设备标识。
	DeviceID string
	// Mode 是账号选择的验证码处理模式：playwright 自动处理或 system_manual 人工完成。
	Mode string
	// ShowBrowser 表示自动模式下是否允许显示浏览器窗口。
	ShowBrowser bool
	// Provider 在验证链接过期时重新取地址；为空表示当前流程不支持刷新。
	Provider browser.TokenCaptchaURLProvider
}

// tokenCaptchaSolveResult 是一次风控求解的结构化结果。
type tokenCaptchaSolveResult struct {
	// Cookies 是验证成功后的新 Cookie 字符串；为空表示未取得有效凭证。
	Cookies string
	// Engine 是本次实际生效的验证分支，用于风控日志区分 playwright/remote/system_manual。
	Engine string
	// Handled 表示本次请求已经被某个验证分支处理；false 表示没有任何可用验证能力。
	Handled bool
	// Err 是求解失败原因；调用方负责把它包装为人工验证地址提示。
	Err error
}

// solveTokenCaptcha 按账号模式选择验证分支。
// system_manual 只走人工浏览器代理，不进入远程或本机自动滑块；playwright 保持历史远程优先、本机兜底顺序。
func (a *Adapter) solveTokenCaptcha(ctx context.Context, input tokenCaptchaSolveInput) tokenCaptchaSolveResult {
	if input.Mode == db.CaptchaBrowserModeSystemManual {
		return a.solveManualTokenCaptcha(ctx, input)
	}
	return a.solveAutomaticTokenCaptcha(ctx, input)
}

// solveManualTokenCaptcha 通过人工验证代理等待用户在系统浏览器完成验证。
// 代理缺失或未返回验证后 Cookie 时返回带验证地址的失败结果，绝不回退到自动滑块。
func (a *Adapter) solveManualTokenCaptcha(ctx context.Context, input tokenCaptchaSolveInput) tokenCaptchaSolveResult {
	// result 预置 system_manual 分支标签，保证审计能区分人工验证与自动验证。
	result := tokenCaptchaSolveResult{Engine: "system_manual", Handled: true}
	if a.manualCaptcha == nil {
		result.Err = &browser.TokenCaptchaFailureError{
			VerificationURL: input.VerificationURL,
			Cause:           errors.New("人工验证不可用：系统未配置人工验证代理"),
		}
		return result
	}
	// cookies、err 保存人工验证代理返回的新 Cookie 与失败原因。
	cookies, err := a.manualCaptcha.VerifyTokenCaptcha(ctx, TokenCaptchaManualVerificationRequest{
		CookieID: input.CookieID, CookieStr: input.CookieStr, VerificationURL: input.VerificationURL, DeviceID: input.DeviceID,
	})
	if err != nil {
		result.Err = &browser.TokenCaptchaFailureError{VerificationURL: input.VerificationURL, Cause: fmt.Errorf("人工验证代理失败: %w", err)}
		return result
	}
	if strings.TrimSpace(cookies) == "" {
		result.Err = &browser.TokenCaptchaFailureError{
			VerificationURL: input.VerificationURL,
			Cause:           errors.New("人工验证不可用：代理未返回验证后的 Cookie"),
		}
		return result
	}
	result.Cookies = cookies
	return result
}

// solveAutomaticTokenCaptcha 保持历史自动验证顺序：先尝试远程过滑块，再回退本机 Playwright/CDP 引擎。
// 未启用浏览器自动化时返回 Handled=false，由调用方发出「无法自动处理」通知。
func (a *Adapter) solveAutomaticTokenCaptcha(ctx context.Context, input tokenCaptchaSolveInput) tokenCaptchaSolveResult {
	// result 预置历史自动分支标签，远程成功时会改写为 remote。
	result := tokenCaptchaSolveResult{Engine: "playwright", Handled: true}
	// headless 是该账号在自动模式下解析出的浏览器可见性。
	headless := browser.ResolveHeadless(input.ShowBrowser)
	if // remoteConfig 用于本次流程后续判断的远程配置
	remoteConfig := a.loadRemoteCaptchaConfig(ctx, input.CookieID); remoteConfig != nil {
		// cookies、handled、err 保存远程过滑块的 Cookie、是否已处理与失败原因。
		cookies, handled, err := solveRemoteCaptcha(
			ctx, newRemoteCaptchaHTTPClient(), *remoteConfig,
			input.CookieID, input.VerificationURL, input.CookieStr, input.DeviceID, input.Provider,
		)
		if handled {
			result.Cookies, result.Engine = cookies, "remote"
			return result
		}
		if err != nil {
			a.logger.Warn("远程过滑块不可用，回退本机逻辑", "account", input.CookieID, "err", err)
		}
	}
	// br、ok 用于本次流程后续判断的br、ok
	br, ok := a.browser.(browserTokenCaptchaRecoverer)
	if a.browser == nil || !ok {
		result.Handled = false
		return result
	}
	if // withEngine、ok 用于本次流程后续判断的withEngine、ok
	withEngine, ok := a.browser.(browserTokenCaptchaEngineRecoverer); ok {
		// cookies、engine、err 保存带引擎标签的本机验证结果。
		cookies, engine, err := withEngine.TokenCaptchaRecoverWithEngine(
			ctx, input.CookieID, input.CookieStr, input.VerificationURL, headless, input.Provider,
		)
		result.Cookies, result.Engine, result.Err = cookies, engine, err
		return result
	}
	// cookies、recoverErr 保存仅支持单一入口的浏览器验证结果。
	cookies, recoverErr := br.TokenCaptchaRecover(
		ctx, input.CookieID, input.CookieStr, input.VerificationURL, headless, input.Provider,
	)
	result.Cookies, result.Err = cookies, recoverErr
	return result
}
