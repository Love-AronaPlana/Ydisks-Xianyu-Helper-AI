package browseragent

import (
	"context"
	"errors"
	"testing"
)

// TestValidateRequestRejectsSensitiveOrUnsafeShapes 验证请求校验拒绝空字段、相对 profile 和非 HTTP 地址。
func TestValidateRequestRejectsSensitiveOrUnsafeShapes(t *testing.T) {
	// absoluteProfileDir 是当前平台上的绝对目录，确保用例只验证目标约束而非平台路径差异。
	absoluteProfileDir := t.TempDir()
	// cases 是必须被拒绝的请求形态：空请求、相对 profile 目录、file 方案地址和缺少主机名的 https 地址。
	cases := []Request{
		{},
		{AccountID: "account", ProfileDir: "relative", VerificationURL: "https://example.com"},
		{AccountID: "account", ProfileDir: absoluteProfileDir, VerificationURL: "file:///secret"},
		{AccountID: "account", ProfileDir: absoluteProfileDir, VerificationURL: "https://"},
	}
	// request 是当前待校验的非法请求样本。
	for _, request := range cases {
		// err 是 ValidateRequest 返回的拒绝原因，必须可被 errors.Is 识别为 ErrInvalidRequest。
		if err := ValidateRequest(request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("request=%+v error=%v, want ErrInvalidRequest", request, err)
		}
	}
}

// TestValidateRequestAcceptsIsolatedHTTPProfile 验证合法请求保留账号隔离和网页地址边界。
func TestValidateRequestAcceptsIsolatedHTTPProfile(t *testing.T) {
	// request 是合法请求：账号隔离目录使用平台绝对路径，验证地址为普通 https 页面。
	request := Request{AccountID: "account-1", ProfileDir: t.TempDir(), VerificationURL: "https://example.com/verify"}
	// err 是合法请求的校验结果，必须为 nil，否则说明边界约束过严会阻断正常验证流程。
	if err := ValidateRequest(request); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

// TestUnavailableAgentNeverClaimsSuccess 验证默认代理不会伪称人工验证已经完成。
func TestUnavailableAgentNeverClaimsSuccess(t *testing.T) {
	// result、err 是默认代理的验证结果与错误；不可用实现必须返回 ErrUnavailable 且不得伪造成功。
	result, err := NewUnavailableAgent().Verify(context.Background(), Request{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v, want ErrUnavailable", err)
	}
	if result.Verified || result.X5sec != "" {
		t.Fatalf("unavailable result=%+v must not contain verification data", result)
	}
}
