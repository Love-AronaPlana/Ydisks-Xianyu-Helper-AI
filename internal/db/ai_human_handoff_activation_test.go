package db

import (
	"testing"
)

// TestActivateHumanHandoffUpsertSemantics 验证关键词触发的接管写入语义：
// 首次写入生效、只延长不缩短、已过期记录可被新窗口覆盖（否则接管会立刻失效或永远无法刷新）。
func TestActivateHumanHandoffUpsertSemantics(t *testing.T) {
	// store、ctx、cleanup 是隔离数据库、共享上下文与释放函数。
	store, ctx, cleanup := newHandoffStore(t)
	defer cleanup()
	// readUntil 读取指定买家的接管截止时间；缺少记录即视为夹具损坏。
	readUntil := func(buyerID string) int64 {
		t.Helper()
		// pausedUntil、found、err 是读取到的截止时间、存在状态与错误。
		pausedUntil, found, err := store.AIReply.GetHumanHandoff(ctx, "cid", buyerID)
		if err != nil || !found {
			t.Fatalf("读取接管记录 (%s)=(%d,%v,%v)", buyerID, pausedUntil, found, err)
		}
		return pausedUntil
	}

	// 首次激活必须写入本次窗口，否则接管从未生效。
	// activateErr 是首次激活的写入结果。
	if activateErr := store.AIReply.ActivateHumanHandoff(ctx, "cid", "buyer-1", 1_000); activateErr != nil {
		t.Fatalf("首次激活失败: %v", activateErr)
	}
	// activated 是首次激活后读回的截止时间。
	if activated := readUntil("buyer-1"); activated != 1_000 {
		t.Fatalf("首次激活截止时间=%d，期望 1000", activated)
	}

	// 买家再次说“人工”时的新窗口更晚，必须覆盖旧值。
	// extendErr 是延长接管的写入结果。
	if extendErr := store.AIReply.ActivateHumanHandoff(ctx, "cid", "buyer-1", 3_000); extendErr != nil {
		t.Fatalf("延长接管失败: %v", extendErr)
	}
	// extended 是延长后读回的截止时间。
	if extended := readUntil("buyer-1"); extended != 3_000 {
		t.Fatalf("更晚窗口应覆盖=%d，期望 3000", extended)
	}

	// 更早的窗口不得缩短既有接管，避免并发或乱序事件提前放行 AI。
	// shortenErr 是写入更早窗口的写入结果。
	if shortenErr := store.AIReply.ActivateHumanHandoff(ctx, "cid", "buyer-1", 2_000); shortenErr != nil {
		t.Fatalf("写入更早接管失败: %v", shortenErr)
	}
	// kept 是写入更早窗口后读回的截止时间，必须保持更晚的原值。
	if kept := readUntil("buyer-1"); kept != 3_000 {
		t.Fatalf("更早窗口不得缩短接管=%d，期望 3000", kept)
	}

	// 已过期的历史记录必须能被新窗口覆盖；否则买家再次说“人工”时接管会立刻失效。
	// historyErr 是写入历史接管记录的写入结果。
	if historyErr := store.AIReply.ActivateHumanHandoff(ctx, "cid", "buyer-expired", 500); historyErr != nil {
		t.Fatalf("写入历史接管失败: %v", historyErr)
	}
	// refreshErr 是覆盖历史接管记录的写入结果。
	if refreshErr := store.AIReply.ActivateHumanHandoff(ctx, "cid", "buyer-expired", 9_000); refreshErr != nil {
		t.Fatalf("覆盖历史接管失败: %v", refreshErr)
	}
	// refreshed 是覆盖后读回的截止时间。
	if refreshed := readUntil("buyer-expired"); refreshed != 9_000 {
		t.Fatalf("过期记录应被新窗口覆盖=%d，期望 9000", refreshed)
	}
	// 过期边界之前必须仍处于接管中，到期后必须放行 AI。
	// activeBefore、beforeErr 是到期前一秒的接管状态与查询错误。
	if activeBefore, beforeErr := store.AIReply.IsHumanHandoffActive(ctx, "cid", "buyer-expired", 8_999); beforeErr != nil || !activeBefore {
		t.Fatalf("到期前应接管中: active=%v err=%v", activeBefore, beforeErr)
	}
	// activeAfter、afterErr 是到期时刻的接管状态与查询错误，边界相等视为已结束。
	if activeAfter, afterErr := store.AIReply.IsHumanHandoffActive(ctx, "cid", "buyer-expired", 9_000); afterErr != nil || activeAfter {
		t.Fatalf("到期后应结束接管: active=%v err=%v", activeAfter, afterErr)
	}
}

// TestConversationHistoryLimitsAndHumanReplyInsert 验证会话历史的条数上限与单条人工回复写入。
func TestConversationHistoryLimitsAndHumanReplyInsert(t *testing.T) {
	// store、ctx、cleanup 是隔离数据库、共享上下文与释放函数。
	store, ctx, cleanup := newHandoffStore(t)
	defer cleanup()
	// 写入 40 轮问答（80 条消息），超过 30 轮上限后只能读回最近 60 条。
	for i := 0; i < 40; i++ {
		// err 是写入第 i 轮对话的失败原因。
		if err := store.AIReply.AddConversationExchange(ctx, "cid", "chat-1", "buyer-1", "item-1",
			AIConversationMessage{Role: "user", Content: "买家消息"},
			AIConversationMessage{Role: "assistant", Content: "AI 回复"},
		); err != nil {
			t.Fatalf("写入历史失败: %v", err)
		}
	}
	// rows、err 是 30 轮上限读回的会话消息与读取错误。
	rows, err := store.AIReply.ConversationHistory(ctx, "cid", "chat-1", "item-1", 60)
	if err != nil || len(rows) != 60 {
		t.Fatalf("默认上限读回 %d 条，期望 60（err=%v）", len(rows), err)
	}
	// 超出硬上限的请求回落到默认值，而不是无限读取。
	if oversized, oversizedErr := store.AIReply.ConversationHistory(ctx, "cid", "chat-1", "item-1", 100000); oversizedErr != nil || len(oversized) != 60 {
		t.Fatalf("超限请求应回落默认值，实际 %d 条（err=%v）", len(oversized), oversizedErr)
	}
	// 人工客服回复以单条助手消息写入，并能被同一上下文读到。
	if insertErr := store.AIReply.AddAIConversationMessage(ctx, "cid", "chat-1", "", "item-1",
		AIConversationMessage{Role: "assistant", Content: "【人工客服】已经帮您处理了", Intent: "human"}); insertErr != nil {
		t.Fatalf("写入人工回复失败: %v", insertErr)
	}
	// refreshed、refreshErr 是写入人工回复后读回的消息。
	refreshed, refreshErr := store.AIReply.ConversationHistory(ctx, "cid", "chat-1", "item-1", 60)
	if refreshErr != nil || len(refreshed) == 0 {
		t.Fatalf("读回人工回复失败: %d 条 err=%v", len(refreshed), refreshErr)
	}
	// last 是最后一条消息，必须就是刚写入的人工客服回复。
	last := refreshed[len(refreshed)-1]
	if last.Role != "assistant" || last.Content != "【人工客服】已经帮您处理了" {
		t.Fatalf("最后一条消息异常: %+v", last)
	}
}
