// human_reply.go 记录人工客服回复到 AI 记忆，供 AI 恢复接话时参考。

package chat

import (
	"context"
	"strings"
)

// AIConversationRecorder 记录人工客服回复到 AI 上下文；未装配时人工回复不进入上下文。
type AIConversationRecorder interface {
	// RecordHumanReply 以 AI 历史中的一条助手消息保存人工客服回复正文。
	RecordHumanReply(ctx context.Context, accountID, chatID, itemID, content string) error
}

// recordHumanReply 在人工客服发送成功后尽力写入 AI 记忆。
// 记录失败只影响 AI 上下文的完整性，绝不影响已经发出的消息，因此这里不向调用方返回错误。
func (s *Service) recordHumanReply(ctx context.Context, session Session, content string) {
	if s == nil || s.repository == nil {
		return
	}
	// text 是去掉首尾空白后的回复正文；空内容没有记录价值。
	text := strings.TrimSpace(content)
	if text == "" || session.AccountID == "" || session.ChatID == "" {
		return
	}
	// recorder 表示当前仓储是否具备 AI 记忆写入能力；缺失时静默跳过。
	recorder, ok := s.repository.(AIConversationRecorder)
	if !ok || recorder == nil {
		return
	}
	// err 是本次写入的失败原因；仅用于说明为何丢弃错误，不改变消息发送结果。
	_ = recorder.RecordHumanReply(ctx, session.AccountID, session.ChatID, session.ItemID, text)
}
