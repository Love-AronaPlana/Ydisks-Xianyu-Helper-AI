// ai_retry.go 处理模型调用的瞬时故障重试：限流、服务端 5xx 与网络超时。
// 上游返回的 429/500/超时通常几秒内自愈，直接放弃会让这次买家消息彻底失去 AI 回复。

package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/sashabaranov/go-openai"
)

// aiRetryDefaultWait 是没有上游提示时的重试等待时间。
const aiRetryDefaultWait = 2 * time.Second

// aiRetryMaxWait 是重试等待上限；上游提示更久时也不会让买家等太久。
const aiRetryMaxWait = 8 * time.Second

// aiRetryHintPattern 提取上游错误文本里的 “Retry after 16s” 提示。
var aiRetryHintPattern = regexp.MustCompile(`(?i)retry after\s+(\d+)\s*s`)

// aiRetryWait 返回本次重试前的等待时间；测试可替换为立即返回，避免真实等待。
var aiRetryWait = defaultAIRetryWait

// defaultAIRetryWait 按上游提示计算等待时间，未提示时使用默认值。
func defaultAIRetryWait(err error) time.Duration {
	if err == nil {
		return aiRetryDefaultWait
	}
	// match 保存上游错误文本中的等待秒数；缺失时按默认等待处理。
	match := aiRetryHintPattern.FindStringSubmatch(err.Error())
	if len(match) < 2 {
		return aiRetryDefaultWait
	}
	// seconds、parseErr 保存解析出的等待秒数与解析错误。
	seconds, parseErr := strconv.Atoi(match[1])
	if parseErr != nil || seconds <= 0 {
		return aiRetryDefaultWait
	}
	// wait 是上游建议的等待时间，必须限制在上限内。
	wait := time.Duration(seconds) * time.Second
	if wait > aiRetryMaxWait {
		return aiRetryMaxWait
	}
	return wait
}

// aiTransientStatus 判断 HTTP 状态码是否属于可重试的瞬时故障（限流与服务端错误）。
func aiTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// isTransientAIError 判断模型调用错误是否值得重试。
// 只重试限流、5xx、网络超时与传输层失败；参数或鉴权错误重试没有意义。
func isTransientAIError(err error) bool {
	if err == nil {
		return false
	}
	// apiErr 保存模型服务返回的结构化错误状态码。
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return aiTransientStatus(apiErr.HTTPStatusCode)
	}
	// reqErr 保存传输层错误；状态码为零表示连接失败或响应体读取失败，同样可重试。
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return reqErr.HTTPStatusCode == 0 || aiTransientStatus(reqErr.HTTPStatusCode)
	}
	// timeoutErr 保存网络层超时；自建中转在预算内没有返回响应头时会走这里。
	var timeoutErr net.Error
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// retryTransientAICall 在瞬时故障时等待一次后重试，最多再调用一次模型。
// firstErr 不是瞬时故障、上下文已取消或重试仍失败时返回原样错误，调用方据此降级到默认回复。
func (a *AIReplierImpl) retryTransientAICall(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	call func([]openai.ChatCompletionMessage) (openai.ChatCompletionResponse, error),
	firstErr error,
) (openai.ChatCompletionResponse, error) {
	if !isTransientAIError(firstErr) {
		return openai.ChatCompletionResponse{}, firstErr
	}
	// wait 是重试前的等待时间；上游给出 “Retry after Ns” 时按提示等待并限制上限。
	wait := aiRetryWait(firstErr)
	a.logger.Warn("AI 调用遇到瞬时故障，稍后重试一次", "wait", wait.String())
	// timer 是重试等待计时器；上下文取消时立即停止等待，不再调用模型。
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return openai.ChatCompletionResponse{}, firstErr
	case <-timer.C:
	}
	return call(messages)
}
