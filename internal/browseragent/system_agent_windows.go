//go:build windows

package browseragent

// NewSystemBrowserAgent 返回当前平台的人工验证代理。
// Windows 使用系统 Chrome/Edge 人工验证；其他平台没有受支持的交互式桌面路径。
func NewSystemBrowserAgent() BrowserAgent {
	return NewWindowsAgent()
}
