package db

import "errors"

const (
	// CaptchaBrowserModePlaywright 表示使用内置 Playwright 浏览器自动处理验证码。
	CaptchaBrowserModePlaywright = "playwright"
	// CaptchaBrowserModeSystemManual 表示停用自动处理并交由系统浏览器人工完成验证码。
	CaptchaBrowserModeSystemManual = "system_manual"
)

// ErrInvalidCaptchaBrowserMode 表示账号验证码处理模式不在受支持枚举内。
var ErrInvalidCaptchaBrowserMode = errors.New("验证码浏览器模式无效")

// IsValidCaptchaBrowserMode 校验账号验证码处理模式，并拒绝空值和未知值。
func IsValidCaptchaBrowserMode(mode string) bool {
	return mode == CaptchaBrowserModePlaywright || mode == CaptchaBrowserModeSystemManual
}
