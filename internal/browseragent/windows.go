//go:build windows

package browseragent

// NewWindowsAgent 构造 Windows 人工验证代理。
// 它在当前用户会话中启动系统 Chrome/Edge，由用户手动完成验证，再通过回环 CDP 读取新的 x5sec。
func NewWindowsAgent() BrowserAgent {
	return NewManualAgent(interactiveSessionLauncher{}, cdpWebSocketCookieClient{}, ManualVerifyOptions{})
}
