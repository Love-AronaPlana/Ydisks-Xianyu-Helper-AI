//go:build !windows

package browseragent

// NewSystemBrowserAgent 返回当前平台的人工验证代理。
// 非 Windows 平台没有受支持的“服务在用户桌面启动系统浏览器”路径，因此明确返回不可用，
// 让账号保持既有的 Playwright 自动验证行为，而不是静默降级或伪造成功。
func NewSystemBrowserAgent() BrowserAgent {
	return UnavailableAgent{}
}
