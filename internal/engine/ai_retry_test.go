package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fastAIRetry 让重试等待立即返回，避免测试真实等待。
func fastAIRetry(t *testing.T) {
	t.Helper()
	// original 保存原有等待函数，测试结束必须还原。
	original := aiRetryWait
	aiRetryWait = func(error) time.Duration { return 0 }
	t.Cleanup(func() { aiRetryWait = original })
}

// TestAIReplyRetriesAfterRateLimit 验证限流响应会触发一次重试并最终拿到回复。
func TestAIReplyRetriesAfterRateLimit(t *testing.T) {
	fastAIRetry(t)
	// store、cleanup 是 AI 重试测试仓储及清理函数。
	store, cleanup := newAIStore(t)
	defer cleanup()
	// ctx 是模型调用使用的测试上下文。
	ctx := context.Background()
	// calls 记录模型服务收到的请求次数。
	var calls int32
	// srv 首次返回 429 与上游重试提示，之后返回正常回复。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded. Retry after 2s."}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": "重试成功"},
		}}})
	}))
	defer srv.Close()
	store.DB.ExecContext(ctx, `INSERT INTO ai_reply_settings (cookie_id, ai_enabled, ai_reply_mode, custom_prompts) VALUES ('cid', 1, 'full', '')`)
	store.Settings.Set(ctx, "ai_api_key", "sk-test")
	store.Settings.Set(ctx, "ai_api_url", srv.URL)
	// res、err 是本次调用的结果与错误；限流后重试必须成功。
	res, err := NewAIReplier("cid", store, nil).Reply(ctx, chatMsg("在吗", "item1", "chat1"))
	if err != nil || res == nil || res.Text != "重试成功" {
		t.Fatalf("限流后应重试成功: res=%+v err=%v", res, err)
	}
	// callCount 是模型服务的实际请求次数，必须正好两次。
	if callCount := atomic.LoadInt32(&calls); callCount != 2 {
		t.Fatalf("模型请求次数=%d，期望 2（首次限流 + 重试）", callCount)
	}
}

// TestAIReplyDoesNotRetryClientError 验证参数或鉴权类 4xx 不触发重试。
func TestAIReplyDoesNotRetryClientError(t *testing.T) {
	fastAIRetry(t)
	// store、cleanup 是 AI 重试测试仓储及清理函数。
	store, cleanup := newAIStore(t)
	defer cleanup()
	// ctx 是模型调用使用的测试上下文。
	ctx := context.Background()
	// calls 记录模型服务收到的请求次数；4xx 不应重试。
	var calls int32
	// srv 固定返回 400，模拟 APIKey 或请求参数错误。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()
	store.DB.ExecContext(ctx, `INSERT INTO ai_reply_settings (cookie_id, ai_enabled, ai_reply_mode, custom_prompts) VALUES ('cid', 1, 'full', '')`)
	store.Settings.Set(ctx, "ai_api_key", "sk-test")
	store.Settings.Set(ctx, "ai_api_url", srv.URL)
	// res、err 是本次调用的结果与错误；4xx 必须直接失败并回退默认回复。
	res, err := NewAIReplier("cid", store, nil).Reply(ctx, chatMsg("在吗", "item1", "chat1"))
	if err == nil || res != nil {
		t.Fatalf("4xx 应直接失败: res=%+v err=%v", res, err)
	}
	// callCount 是模型服务的实际请求次数，必须保持一次。
	if callCount := atomic.LoadInt32(&calls); callCount != 1 {
		t.Fatalf("模型请求次数=%d，期望 1（4xx 不重试）", callCount)
	}
}

// TestAIRetryWaitUsesUpstreamHint 验证重试等待会采用上游提示并限制上限。
func TestAIRetryWaitUsesUpstreamHint(t *testing.T) {
	// 上游提示 2 秒时按提示等待。
	if got := defaultAIRetryWait(retryTestError("status 429: Rate limit exceeded. Retry after 2s.")); got != 2*time.Second {
		t.Fatalf("提示 2 秒应等待 2 秒，实际 %s", got)
	}
	// 上游提示超过上限时截断到上限，避免买家等待过久。
	if got := defaultAIRetryWait(retryTestError("Retry after 600s")); got != aiRetryMaxWait {
		t.Fatalf("超长提示应截断为 %s，实际 %s", aiRetryMaxWait, got)
	}
	// 没有提示时使用默认等待时间。
	if got := defaultAIRetryWait(retryTestError("connection reset by peer")); got != aiRetryDefaultWait {
		t.Fatalf("无提示应使用默认等待 %s，实际 %s", aiRetryDefaultWait, got)
	}
}

// retryTestError 是只带文本的测试错误，用于构造上游错误提示。
type retryTestError string

// Error 实现 error 接口。
func (e retryTestError) Error() string { return string(e) }
