package chat

import (
	"context"
	"errors"
	"testing"
)

// humanReplyRepositoryFake 是记录人工回复写入请求的仓储替身。
type humanReplyRepositoryFake struct {
	// Repository 让替身满足聊天服务的基础仓储能力。
	Repository
	// recorded 保存最后一次写入的账号、会话、商品与内容。
	recorded struct {
		accountID string
		chatID    string
		itemID    string
		content   string
	}
	// calls 记录写入调用次数；未装配记录能力时保持为零。
	calls int
	// err 控制写入失败，用于验证失败不影响发送流程。
	err error
}

// RecordHumanReply 记录人工回复写入请求并返回预设错误。
func (f *humanReplyRepositoryFake) RecordHumanReply(_ context.Context, accountID, chatID, itemID, content string) error {
	f.calls++
	f.recorded.accountID = accountID
	f.recorded.chatID = chatID
	f.recorded.itemID = itemID
	f.recorded.content = content
	return f.err
}

// TestRecordHumanReplyWritesAIContext 验证人工客服回复按账号、会话与商品写入 AI 上下文。
func TestRecordHumanReplyWritesAIContext(t *testing.T) {
	// repository 是带记录能力的内存仓储。
	repository := &humanReplyRepositoryFake{}
	// service 是注入该仓储的聊天服务。
	service := &Service{repository: repository}
	// session 是人工回复所属的会话摘要。
	session := Session{AccountID: "cid", ChatID: "chat-1", ItemID: "item-1", PeerUserID: "buyer-1"}
	service.recordHumanReply(context.Background(), session, "  已经帮您处理了  ")
	if repository.calls != 1 {
		t.Fatalf("人工回复写入次数=%d，期望 1", repository.calls)
	}
	if repository.recorded.content != "已经帮您处理了" || repository.recorded.itemID != "item-1" || repository.recorded.chatID != "chat-1" {
		t.Fatalf("写入内容异常: %+v", repository.recorded)
	}
}

// TestRecordHumanReplySkipsEmptyAndUnavailable 验证空内容与缺少记录能力的仓储都不会写入。
func TestRecordHumanReplySkipsEmptyAndUnavailable(t *testing.T) {
	// ctx 是写入调用共用的上下文。
	ctx := context.Background()
	// session 是人工回复所属的会话摘要。
	session := Session{AccountID: "cid", ChatID: "chat-1", ItemID: "item-1"}
	// repository 是带记录能力的内存仓储。
	repository := &humanReplyRepositoryFake{}
	// service 是注入该仓储的聊天服务。
	service := &Service{repository: repository}
	// 空内容没有记录价值。
	service.recordHumanReply(ctx, session, "   ")
	// 缺少账号或会话标识时无法定位上下文。
	service.recordHumanReply(ctx, Session{ChatID: "chat-1"}, "内容")
	if repository.calls != 0 {
		t.Fatalf("空内容或缺失标识时不应写入，实际 %d 次", repository.calls)
	}
	// 仓储不具备记录能力时静默跳过，不影响消息发送。
	plain := &Service{repository: plainRepository{}}
	plain.recordHumanReply(ctx, session, "内容")
	// 写入失败必须被吞掉，绝不冒泡到发送流程。
	failing := &Service{repository: &humanReplyRepositoryFake{err: errors.New("写库失败")}}
	failing.recordHumanReply(ctx, session, "内容")
}

// plainRepository 是不具备 AI 上下文记录能力的仓储替身。
type plainRepository struct {
	// Repository 只提供聊天服务所需的基础能力。
	Repository
}
