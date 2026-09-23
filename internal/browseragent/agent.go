// Package browseragent 定义人工浏览器验证的最小能力端口；该包不读取、保存或记录账号凭证。
package browseragent

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

var (
	// ErrUnavailable 表示人工浏览器代理尚未由桌面托盘与服务 IPC 接通。
	ErrUnavailable = errors.New("人工浏览器代理不可用：Windows 托盘 IPC 尚未接入")
	// ErrInvalidRequest 表示验证请求缺少安全边界所需的信息或包含不允许的地址。
	ErrInvalidRequest = errors.New("人工浏览器验证请求无效")
)

// Request 描述一次人工验证任务；它刻意不包含 Cookie、Token、密码或完整凭证字符串。
type Request struct {
	// AccountID 标识需要验证的本地账号，供独立 profile 归属和审计关联使用。
	AccountID string
	// ProfileDir 是该账号专用的绝对浏览器用户目录；代理不得复用其他账号目录。
	ProfileDir string
	// VerificationURL 是服务取得的待验证地址；它只允许 http 或 https 方案。
	VerificationURL string
	// KnownX5sec 是账号当前已持有的 x5sec 值集合；用于判定人工验证是否产生了全新值。
	KnownX5sec []string
}

// Result 是人工验证的最小回传结果；X5sec 只供调用方完成当前协议重试，不得写日志或持久化到普通状态。
type Result struct {
	// Verified 表示用户完成验证且代理确认页面已离开验证状态。
	Verified bool
	// X5sec 是平台签发的新验证 Cookie 值；为空表示没有可安全回传的更新值。
	X5sec string
}

// BrowserAgent 定义服务向交互桌面请求人工验证的消费者端口。
type BrowserAgent interface {
	// Verify 在当前交互用户会话中打开独立账号 profile 并等待人工完成验证；实现不得记录凭证。
	Verify(context.Context, Request) (Result, error)
}

// ValidateRequest 校验人工代理请求的账号隔离、profile 路径和验证地址。
func ValidateRequest(request Request) error {
	if strings.TrimSpace(request.AccountID) == "" || strings.TrimSpace(request.ProfileDir) == "" {
		return ErrInvalidRequest
	}
	if !filepath.IsAbs(request.ProfileDir) {
		return ErrInvalidRequest
	}
	// parsedURL 是解析后的验证地址（仅接受 http/https 且必须带主机名），err 是地址无法解析时的错误；两者任一不满足即按请求无效拒绝。
	parsedURL, err := url.Parse(strings.TrimSpace(request.VerificationURL))
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return ErrInvalidRequest
	}
	return nil
}
