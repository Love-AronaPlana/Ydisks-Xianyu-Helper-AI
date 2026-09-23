package browseragent

import "context"

// UnavailableAgent 是未接入桌面托盘 IPC 时使用的明确失败实现；它不会伪造验证成功。
type UnavailableAgent struct{}

// Verify 明确拒绝未接入的人工验证请求，并且不接触请求中的凭证或地址内容。
func (UnavailableAgent) Verify(ctx context.Context, request Request) (Result, error) {
	return Result{}, ErrUnavailable
}

// NewUnavailableAgent 构造一个始终返回 ErrUnavailable 的安全默认代理。
func NewUnavailableAgent() BrowserAgent {
	return UnavailableAgent{}
}
