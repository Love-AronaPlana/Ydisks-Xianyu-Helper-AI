package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"xianyu-go/internal/automation"
	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/ws"
)

// fakeAPIReplier 可控的 API 回复 mock：返回预设结果或错误。
type fakeAPIReplier struct {
	result *ReplyResult
	err    error
	called int
}

// Reply 封装回复业务协调。
func (f *fakeAPIReplier) Reply(_ context.Context, _ ChatMessage) (*ReplyResult, error) {
	f.called++
	return f.result, f.err
}

// fakeAIReplier 可控的 AI 回复 mock。
type fakeAIReplier struct {
	result *ReplyResult
	err    error
	called int
}

// Reply 封装回复业务协调。
func (f *fakeAIReplier) Reply(_ context.Context, _ ChatMessage) (*ReplyResult, error) {
	f.called++
	return f.result, f.err
}

// TestReply_HumanHandoffPreservesKeywordAndBlocksAI 验证人工触发消息仍按关键词优先回复，并为同一买家暂停 AI。
func TestReply_HumanHandoffPreservesKeywordAndBlocksAI(t *testing.T) {
	// store、cleanup 提供本测试独占的账号与人工接管表。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是人工接管配置、状态和回复解析共用的测试上下文。
	ctx := context.Background()
	// setupErr 保存启用 AI 及十分钟人工接管窗口的配置写入结果。
	setupErr := store.AIReply.UpsertSettings(ctx, "cid", db.AIReplySettings{AIEnabled: true, HumanHandoffMinutes: 10})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	// keywordErr 保存人工触发消息的关键词回复配置结果。
	_, keywordErr := store.DB.ExecContext(ctx, `INSERT INTO keywords (cookie_id,keyword,reply,type) VALUES ('cid','人工','关键词仍回复','text')`)
	if keywordErr != nil {
		t.Fatal(keywordErr)
	}
	// ai 记录人工接管期间不应被调用的 AI 请求次数。
	ai := &fakeAIReplier{result: &ReplyResult{Text: "不应调用 AI"}}
	// reply 使用固定时间验证人工接管截止时间的确定性。
	reply := NewReplyService("cid", store, nil, nil, ai, nil)
	reply.now = func() time.Time { return time.Unix(1_000, 0).UTC() }
	// trigger 是包含人工关键词的当前买家消息；它仍应返回关键词回复。
	trigger := chatMsg("请转人工", "", "chat-trigger")
	trigger.SenderUserID = "buyer-a"
	// result 是人工接管触发消息的回复结果，必须仍走关键词分支且忽略 AI。
	result := reply.resolve(ctx, trigger)
	if result == nil || result.Source != "关键词" || result.Text != "关键词仍回复" {
		t.Fatalf("人工触发消息应保留关键词回复: %+v", result)
	}
	if ai.called != 0 {
		t.Fatalf("人工接管触发消息不应调用 AI: %d", ai.called)
	}
	// active、activeErr 保存当前买家人工接管状态及查询错误。
	active, activeErr := store.AIReply.IsHumanHandoffActive(ctx, "cid", "buyer-a", 1_000+9*60)
	if activeErr != nil || !active {
		t.Fatalf("应保存十分钟接管状态 active=%v err=%v", active, activeErr)
	}
	// defaultErr 保存默认回复配置写入结果，验证接管期间 AI 跳过后仍回退默认回复。
	defaultErr := store.DefaultReps.Upsert(ctx, "cid", db.DefaultReply{Enabled: true, ReplyContent: "默认回复"})
	if defaultErr != nil {
		t.Fatal(defaultErr)
	}
	// resumed 保存同一买家另一会话在接管期间的回复结果。
	resumed := reply.resolve(ctx, ChatMessage{AccountID: "cid", ChatID: "chat-other", SenderUserID: "buyer-a", Text: "普通问题"})
	if resumed == nil || resumed.Source != "默认" || ai.called != 0 {
		t.Fatalf("接管期间应跳过 AI 并回退默认: result=%+v calls=%d", resumed, ai.called)
	}
	// otherBuyer 保存不同买家消息，确认人工接管不会跨买家泄漏。
	otherBuyer := reply.resolve(ctx, ChatMessage{AccountID: "cid", ChatID: "chat-other", SenderUserID: "buyer-b", Text: "普通问题"})
	if otherBuyer == nil || otherBuyer.Source != "AI" || ai.called != 1 {
		t.Fatalf("不同买家应继续调用 AI: result=%+v calls=%d", otherBuyer, ai.called)
	}
}

// TestReply_ManualHumanHandoffBlocksAIAndClearResumes 验证会话界面手动接管会跳过 AI，提前结束后立即恢复。
func TestReply_ManualHumanHandoffBlocksAIAndClearResumes(t *testing.T) {
	// store、cleanup 提供本测试独占的人工接管状态。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是接管写入、清除和回复解析共用的测试上下文。
	ctx := context.Background()
	// setupErr 保存启用 AI 的账号级配置写入结果；手动接管不依赖账号级“人工”关键词配置。
	if setupErr := store.AIReply.UpsertSettings(ctx, "cid", db.AIReplySettings{AIEnabled: true}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// ai 记录手动接管期间与结束后 AI 的实际调用次数。
	ai := &fakeAIReplier{result: &ReplyResult{Text: "AI 已恢复"}}
	// reply 使用固定时间，使接管窗口判定不依赖真实时间流逝。
	reply := NewReplyService("cid", store, nil, nil, ai, nil)
	reply.now = func() time.Time { return time.Unix(4_000, 0).UTC() }
	// handoffErr 保存会话界面手动接管的写入结果。
	if handoffErr := store.AIReply.SetHumanHandoff(ctx, "cid", "buyer-manual", 4_600); handoffErr != nil {
		t.Fatal(handoffErr)
	}
	// blocked 保存接管期间的回复结果；没有默认回复时必须为空且不调用 AI。
	blocked := reply.resolve(ctx, ChatMessage{AccountID: "cid", ChatID: "chat-manual", SenderUserID: "buyer-manual", Text: "在吗"})
	if blocked != nil || ai.called != 0 {
		t.Fatalf("手动接管期间不得调用 AI: result=%+v calls=%d", blocked, ai.called)
	}
	// clearErr 保存提前结束接管的写入结果。
	if _, clearErr := store.AIReply.ClearHumanHandoff(ctx, "cid", "buyer-manual"); clearErr != nil {
		t.Fatal(clearErr)
	}
	// resumed 保存结束接管后的回复结果；AI 必须立即恢复。
	resumed := reply.resolve(ctx, ChatMessage{AccountID: "cid", ChatID: "chat-manual", SenderUserID: "buyer-manual", Text: "在吗"})
	if resumed == nil || resumed.Source != "AI" || ai.called != 1 {
		t.Fatalf("结束接管后应立即恢复 AI: result=%+v calls=%d", resumed, ai.called)
	}
	// otherBuyer 保存同一账号下其它买家的回复结果，用于确认接管按买家隔离。
	otherBuyer := reply.resolve(ctx, ChatMessage{AccountID: "cid", ChatID: "chat-manual", SenderUserID: "buyer-other", Text: "在吗"})
	if otherBuyer == nil || otherBuyer.Source != "AI" || ai.called != 2 {
		t.Fatalf("其它买家不受接管影响: result=%+v calls=%d", otherBuyer, ai.called)
	}
}

// TestReply_HumanHandoffExpiresAndAIResumes 验证截止时间相等及超过后均恢复 AI 回复。
// TestReply_HumanHandoffExpiresAndAIResumes 验证人工接管到期后 AI 恢复，并清理过期记录。
func TestReply_HumanHandoffExpiresAndAIResumes(t *testing.T) {
	// store、cleanup 提供本测试独占的人工接管状态。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是状态写入和回复解析共用的测试上下文。
	ctx := context.Background()
	// setupErr 保存一分钟人工接管配置的写入结果。
	if setupErr := store.AIReply.UpsertSettings(ctx, "cid", db.AIReplySettings{AIEnabled: true, HumanHandoffMinutes: 1}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// activateErr 保存人工接管截止时间的直接写入结果。
	if activateErr := store.AIReply.ActivateHumanHandoff(ctx, "cid", "buyer-a", 2_060); activateErr != nil {
		t.Fatal(activateErr)
	}
	// ai 返回固定内容，供恢复调用断言。
	ai := &fakeAIReplier{result: &ReplyResult{Text: "AI恢复"}}
	// reply 使用截止时间边界前后的固定时钟。
	reply := NewReplyService("cid", store, nil, nil, ai, nil)
	// reply.now 先停在截止时间之前，验证严格大于 now 才算仍在接管期内。
	reply.now = func() time.Time { return time.Unix(2_059, 0).UTC() }
	// beforeExpiryMessage 是接管窗口内的普通买家消息。
	beforeExpiryMessage := chatMsg("普通问题", "", "chat-expire-2")
	beforeExpiryMessage.SenderUserID = "buyer-a"
	// beforeExpiry 是仍在接管窗口内（严格小于截止秒）时的解析结果，此时必须跳过 AI 并返回空结果。
	beforeExpiry := reply.resolve(ctx, beforeExpiryMessage)
	if beforeExpiry != nil || ai.called != 0 {
		t.Fatalf("截止前应跳过 AI 并继续默认空结果: result=%+v calls=%d", beforeExpiry, ai.called)
	}
	// reply.now 推到截止秒，验证边界相等时暂停已结束。
	reply.now = func() time.Time { return time.Unix(2_060, 0).UTC() }
	// atBoundaryMessage 是截止秒之后的买家消息。
	atBoundaryMessage := chatMsg("普通问题", "", "chat-expire")
	atBoundaryMessage.SenderUserID = "buyer-a"
	// atBoundary 是截止秒恰好相等时的解析结果，此时暂停已结束必须恢复 AI 回复。
	atBoundary := reply.resolve(ctx, atBoundaryMessage)
	if atBoundary == nil || atBoundary.Source != "AI" || ai.called != 1 {
		t.Fatalf("截止时间相等应恢复 AI: result=%+v calls=%d", atBoundary, ai.called)
	}
	// 到期放行 AI 的同时必须清理过期记录，避免历史接管行无限累积并让恢复时刻在日志中可见。
	if _, found, readErr := store.AIReply.GetHumanHandoff(ctx, "cid", "buyer-a"); readErr != nil || found {
		t.Fatalf("过期接管记录应被清理 found=%v err=%v", found, readErr)
	}
}

// TestReply_HumanHandoffNormalizesBuyerIdentity 验证同一个买家的带后缀与不带后缀标识共享同一个人工接管窗口。
func TestReply_HumanHandoffNormalizesBuyerIdentity(t *testing.T) {
	// store、cleanup 提供本测试独占的人工接管状态。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是状态写入和回复解析共用的测试上下文。
	ctx := context.Background()
	// setupErr 保存二十分钟人工接管配置的写入结果。
	if setupErr := store.AIReply.UpsertSettings(ctx, "cid", db.AIReplySettings{AIEnabled: true, HumanHandoffMinutes: 20}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// ai 记录被错误调用的次数，接管期间必须保持为零。
	ai := &fakeAIReplier{result: &ReplyResult{Text: "不应调用 AI"}}
	// reply 使用固定时钟，避免依赖真实时间流逝。
	reply := NewReplyService("cid", store, nil, nil, ai, nil)
	reply.now = func() time.Time { return time.Unix(5_000, 0).UTC() }
	// trigger 的发送者带平台后缀，代表平台展示扩展中的常见形态。
	trigger := chatMsg("请转人工", "", "chat-suffix")
	trigger.SenderUserID = "buyer-a@goofish"
	// resolve 触发人工接管；该消息本身仍走默认空结果。
	// triggerResult 是人工触发消息的解析结果，必须为空且不调用 AI。
	if triggerResult := reply.resolve(ctx, trigger); triggerResult != nil || ai.called != 0 {
		t.Fatalf("触发消息不应调用 AI: result=%+v calls=%d", triggerResult, ai.called)
	}
	// 同一买家后续消息不带后缀时，必须命中同一个接管窗口并被跳过 AI。
	followUp := chatMsg("在吗", "", "chat-suffix")
	followUp.SenderUserID = "buyer-a"
	// followUpResult 是归一后同一买家消息的解析结果，必须同样被跳过。
	if followUpResult := reply.resolve(ctx, followUp); followUpResult != nil || ai.called != 0 {
		t.Fatalf("归一后应命中同一接管窗口: result=%+v calls=%d", followUpResult, ai.called)
	}
	// 会话页面按不带后缀的对端标识读取接管状态时，也必须看到同一个窗口。
	// pausedUntil、found、readErr 是归一键读到的截止时间、存在状态与查询错误。
	if pausedUntil, found, readErr := store.AIReply.GetHumanHandoff(ctx, "cid", "buyer-a"); readErr != nil || !found || pausedUntil <= 5_000 {
		t.Fatalf("归一后的接管窗口=(%d,%v,%v)", pausedUntil, found, readErr)
	}
}

// TestNormalizeHumanHandoffBuyerID 验证人工接管隔离键会去掉空白与平台后缀。
func TestNormalizeHumanHandoffBuyerID(t *testing.T) {
	// cases 覆盖平台可能出现的标识形态。
	cases := []struct {
		// raw 是平台返回的原始发送者标识。
		raw string
		// want 是归一后用于人工接管的隔离键。
		want string
	}{
		{raw: "buyer-a", want: "buyer-a"},
		{raw: "buyer-a@goofish", want: "buyer-a"},
		{raw: "  buyer-a@goofish  ", want: "buyer-a"},
		{raw: "3000000000001", want: "3000000000001"},
		{raw: "", want: ""},
		{raw: "@goofish", want: ""},
	}
	// current 是当前待校验的标识形态。
	for _, current := range cases {
		// normalized 是归一后的隔离键，必须与期望值一致。
		if normalized := NormalizeHumanHandoffBuyerID(current.raw); normalized != current.want {
			t.Fatalf("归一化 %q=%q，期望 %q", current.raw, normalized, current.want)
		}
	}
}

// TestReply_HumanHandoffDatabaseFailuresFailClosed 验证人工配置读取和接管写入失败时均不调用 AI。
func TestReply_HumanHandoffDatabaseFailuresFailClosed(t *testing.T) {
	// store、cleanup 提供本测试独占的数据库连接。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是数据库故障夹具和回复解析共用的测试上下文。
	ctx := context.Background()
	// setupErr 保存启用 AI 及人工接管窗口的配置写入结果。
	if setupErr := store.AIReply.UpsertSettings(ctx, "cid", db.AIReplySettings{AIEnabled: true, HumanHandoffMinutes: 5}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// ai 记录 fail-closed 时不得发生的调用。
	ai := &fakeAIReplier{result: &ReplyResult{Text: "不应调用 AI"}}
	// reply 使用固定时间，避免故障测试依赖系统时钟。
	reply := NewReplyService("cid", store, nil, nil, ai, nil)
	reply.now = func() time.Time { return time.Unix(3_000, 0).UTC() }
	// triggerErr 保存拒绝人工接管写入的 SQLite 触发器创建结果。
	if _, triggerErr := store.DB.ExecContext(ctx, `CREATE TRIGGER deny_handoff_write BEFORE INSERT ON ai_human_handoffs BEGIN SELECT RAISE(FAIL,'handoff write rejected'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	// writeFailure 保存接管写入失败时的回复结果；AI 必须被阻断。
	writeFailure := reply.resolve(ctx, chatMsg("需要人工", "", "chat-write-failure"))
	if writeFailure != nil || ai.called != 0 {
		t.Fatalf("接管写入失败应 fail-closed: result=%+v calls=%d", writeFailure, ai.called)
	}
	// closeErr 保存关闭数据库连接的结果，后续配置读取必然失败。
	if closeErr := store.DB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// readFailure 保存人工配置读取失败时的回复结果；数据库不确定时仍不得调用 AI。
	readFailure := reply.resolve(ctx, chatMsg("需要人工", "", "chat-read-failure"))
	if readFailure != nil || ai.called != 0 {
		t.Fatalf("接管配置读取失败应 fail-closed: result=%+v calls=%d", readFailure, ai.called)
	}
}

// recordingSender 记录发送的文本/图片，用于断言回复投递。
type recordingSender struct {
	texts    []textSent
	images   []imageSent
	textErr  error
	imageErr error
	// textCalls 统计文本发送尝试次数，包含返回错误的传输调用。
	textCalls int
	// beforeTextError 在文本发送返回错误前执行，用于模拟发送过程中取消请求上下文。
	beforeTextError func()
}

// SendReply 复现聊天应用的图片先发、文字后发顺序，供回复状态测试使用。
func (r *recordingSender) SendReply(ctx context.Context, message ReplyMessage) (ReplySendResult, error) {
	// result 保存已成功完成的平台分段。
	result := ReplySendResult{}
	if message.ImageURL != "" {
		// imageErr 保存图片分段发送结果。
		if imageErr := r.SendImage(ctx, message.ChatID, message.ToUserID, message.ImageURL, 0, 0, 0); imageErr != nil {
			result.Uncertain = replySendUncertain(imageErr)
			return result, imageErr
		}
		result.ImageSent = true
	}
	if message.Text != "" {
		// textErr 保存文字分段发送结果。
		if textErr := r.SendText(ctx, message.ChatID, message.ToUserID, message.Text); textErr != nil {
			result.Uncertain = replySendUncertain(textErr)
			return result, textErr
		}
		result.TextSent = true
	}
	return result, nil
}

// replySendUncertain 复现聊天应用对测试发送错误的确定性分类；普通本地错误和明确未发送错误都允许重试。
func replySendUncertain(err error) bool {
	if err == nil || errors.Is(err, automation.ErrMessageNotSent) {
		return false
	}
	// sendErr 保存可由协议层明确标记为不确定的发送错误。
	var sendErr *ws.SendError
	return errors.As(err, &sendErr) && ws.SendResultKind(err) == ws.SendUncertain
}

// recordingReplyDelivery 记录引擎交给聊天应用的完整回复，不模拟任何协议级图片尺寸逻辑。
type recordingReplyDelivery struct {
	// messages 保存完整回复消息及其字段。
	messages []ReplyMessage
	// result 保存聊天应用返回的分段确认结果。
	result ReplySendResult
	// err 保存聊天应用返回的发送错误。
	err error
}

// SendReply 记录完整回复并返回预设的应用层发送结果。
func (d *recordingReplyDelivery) SendReply(_ context.Context, message ReplyMessage) (ReplySendResult, error) {
	d.messages = append(d.messages, message)
	return d.result, d.err
}

// textSent 用于本次流程后续判断的文本Sent
type textSent struct {
	chatID, toUserID, text string
}

// imageSent 用于本次流程后续判断的图片Sent
type imageSent struct {
	chatID, toUserID, url string
	cardID                int64
	// width 和 height 保存交给 WebSocket 的图片像素尺寸。
	width, height int
}

// SendText 封装Send文本业务协调。
func (r *recordingSender) SendText(_ context.Context, chatID, toUserID, text string) error {
	r.textCalls++
	if r.beforeTextError != nil {
		r.beforeTextError()
	}
	if r.textErr != nil {
		return r.textErr
	}
	r.texts = append(r.texts, textSent{chatID, toUserID, text})
	return nil
}

// TestReplyOnceMarksDefiniteFailureWithIndependentContext 验证取消发送上下文时确定未发送状态仍可被领取重试。
func TestReplyOnceMarksDefiniteFailureWithIndependentContext(t *testing.T) {
	// store、cleanup 保存隔离数据库及关闭责任。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// setupErr 保存启用一次性默认回复的配置写入错误。
	if setupErr := store.DefaultReps.Upsert(context.Background(), "cid", db.DefaultReply{Enabled: true, ReplyOnce: true, ReplyContent: "欢迎"}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// requestCtx、cancel 保存会在发送失败期间被取消的原始请求上下文。
	requestCtx, cancel := context.WithCancel(context.Background())
	// sender 模拟确定未发送错误，并在返回前取消原始请求上下文。
	sender := &recordingSender{textErr: automation.ErrMessageNotSent, beforeTextError: cancel}
	// service 使用真实状态仓储验证失败状态可恢复。
	service := NewReplyService("cid", store, sender, nil, nil, nil)
	// sendErr 保存确定未发送错误的回复结果。
	if sendErr := service.Handle(requestCtx, chatMsg("你好", "", "chat-definite-failure")); !errors.Is(sendErr, automation.ErrMessageNotSent) {
		t.Fatalf("确定未发送错误未透传: %v", sendErr)
	}
	// record、recordErr 保存第一次失败后的状态。
	record, recordErr := store.DefaultReps.Record(context.Background(), "cid", "chat-definite-failure")
	if recordErr != nil || record.Status != "failed" {
		t.Fatalf("取消上下文不应遗留 sending 状态 record=%+v err=%v", record, recordErr)
	}
	// sender 恢复成功发送，验证 failed 记录可被下一次请求重新领取。
	sender.textErr = nil
	// retryErr 保存重新领取失败状态后的回复结果。
	if retryErr := service.Handle(context.Background(), chatMsg("还在吗", "", "chat-definite-failure")); retryErr != nil || sender.textCalls != 2 {
		t.Fatalf("失败状态无法重新领取 retryErr=%v calls=%d", retryErr, sender.textCalls)
	}
}

// SendImage 记录聊天图片地址、关联卡密和像素尺寸，供回复投递测试断言。
// ctx 是发送上下文；chatID/toUserID/url/cardID 标识消息身份；width/height 是发送协议中的图片像素尺寸。
func (r *recordingSender) SendImage(_ context.Context, chatID, toUserID, url string, cardID int64, width, height int) error {
	if r.imageErr != nil {
		return r.imageErr
	}
	r.images = append(r.images, imageSent{chatID: chatID, toUserID: toUserID, url: url, cardID: cardID, width: width, height: height})
	return nil
}

// TestAIQuoteSavedOnlyAfterTextDelivery 验证 AI 报价只有在回复发送成功后才成为可执行报价。
func TestAIQuoteSavedOnlyAfterTextDelivery(t *testing.T) {
	// store、cleanup 是回复链测试仓储及清理函数。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是回复发送与报价领取共用的测试上下文。
	ctx := context.Background()
	// result 是模拟 AI 已给出 9.90 元有效报价的回复结果。
	result := &ReplyResult{Text: "可以，9.90 元成交", AutoPriceQuote: &AIPriceQuoteProposal{PriceCents: 990}}
	// failedSender 模拟文本没有成功交给买家。
	failedSender := &recordingSender{textErr: errors.New("send failed")}
	// failedService 是注入发送失败替身的 AI 回复链。
	failedService := NewReplyService("cid", store, failedSender, nil, &fakeAIReplier{result: result}, nil)
	// err 是模拟发送失败时必须向调用方返回的错误。
	if err := failedService.Handle(ctx, chatMsg("能便宜吗", "item-1", "chat-1")); err == nil {
		t.Fatal("发送失败应返回错误")
	}
	// failedQuote 是发送失败后尝试领取的报价，必须为空。
	failedQuote, err := store.AIReply.ClaimPendingQuote(ctx, "cid", "chat-1", "buyer1", "item-1", "order-failed", time.Now().Unix())
	if err != nil || failedQuote != nil {
		t.Fatalf("发送失败不应保存报价: quote=%+v err=%v", failedQuote, err)
	}
	// successService 是文本发送成功的 AI 回复链。
	successService := NewReplyService("cid", store, &recordingSender{}, nil, &fakeAIReplier{result: result}, nil)
	if err = successService.Handle(ctx, chatMsg("能便宜吗", "item-1", "chat-1")); err != nil {
		t.Fatal(err)
	}
	// successQuote 是发送成功后与订单事实匹配的可执行报价。
	successQuote, err := store.AIReply.ClaimPendingQuote(ctx, "cid", "chat-1", "buyer1", "item-1", "order-success", time.Now().Unix())
	if err != nil || successQuote == nil || successQuote.PriceCents != 990 {
		t.Fatalf("发送成功应保存报价: quote=%+v err=%v", successQuote, err)
	}
}

// TestReplyOnceRetriesOnlyFailedParts 封装Test回复OnceRetriesOnly失败Parts业务协调。
func TestReplyOnceRetriesOnlyFailedParts(t *testing.T) {
	// s、cleanup 用于本次流程后续判断的s、cleanup
	s, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 用于本次流程后续判断的ctx
	ctx := context.Background()
	s.DB.ExecContext(ctx, `INSERT INTO default_replies
		(cookie_id,enabled,reply_content,reply_image_url,reply_once)
		VALUES ('cid',1,'文字','http://img/retry.png',1)`)

	// textFailure 用于本次流程后续判断的文本Failure
	textFailure := errors.New("text failed")
	// firstSender 用于本次流程后续判断的firstSender
	firstSender := &recordingSender{textErr: textFailure}
	// service 用于本次流程后续判断的service
	service := NewReplyService("cid", s, firstSender, nil, nil, nil)
	if // err 用于本次流程后续判断的err
	err := service.Handle(ctx, chatMsg("在吗", "", "chat-retry")); !errors.Is(err, textFailure) {
		t.Fatalf("first error=%v want text failure", err)
	}
	if len(firstSender.images) != 1 || len(firstSender.texts) != 0 {
		t.Fatalf("first delivery images=%+v texts=%+v", firstSender.images, firstSender.texts)
	}
	// record、err 用于本次流程后续判断的record、err
	record, err := s.DefaultReps.Record(ctx, "cid", "chat-retry")
	if err != nil || record.Status != "failed" || !record.ImageSent || record.TextSent {
		t.Fatalf("failed record=%+v err=%v", record, err)
	}

	// secondSender 用于本次流程后续判断的secondSender
	secondSender := &recordingSender{}
	service = NewReplyService("cid", s, secondSender, nil, nil, nil)
	if // err 用于本次流程后续判断的err
	err := service.Handle(ctx, chatMsg("再问", "", "chat-retry")); err != nil {
		t.Fatal(err)
	}
	if len(secondSender.images) != 0 || len(secondSender.texts) != 1 {
		t.Fatalf("retry should send text only: images=%+v texts=%+v", secondSender.images, secondSender.texts)
	}
	record, err = s.DefaultReps.Record(ctx, "cid", "chat-retry")
	if err != nil || record.Status != "sent" || !record.ImageSent || !record.TextSent {
		t.Fatalf("sent record=%+v err=%v", record, err)
	}
}

// TestReplyOnceQuarantinesUncertainSend 验证平台可能已送达时不允许一次性默认回复自动重发。
func TestReplyOnceQuarantinesUncertainSend(t *testing.T) {
	// store、cleanup 保存隔离数据库及其关闭责任。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是默认回复配置和发送状态读写使用的无截止上下文。
	ctx := context.Background()
	// setupErr 保存启用一次性默认回复的配置写入错误。
	setupErr := store.DefaultReps.Upsert(ctx, "cid", db.DefaultReply{Enabled: true, ReplyOnce: true, ReplyContent: "欢迎"})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	// sender 模拟平台连接在发送后断开，无法判断消息是否已经送达。
	sender := &recordingSender{textErr: &ws.SendError{Kind: ws.SendUncertain}}
	// service 使用真实投递记录存储验证不确定结果隔离。
	service := NewReplyService("cid", store, sender, nil, nil, nil)
	// firstErr 保存首次发送返回的不确定错误。
	firstErr := service.Handle(ctx, chatMsg("你好", "", "chat-uncertain"))
	if firstErr == nil {
		t.Fatal("不确定发送结果必须返回调用错误")
	}
	// secondErr 保存同一会话再次触发时的结果；它不应发出第二条消息。
	secondErr := service.Handle(ctx, chatMsg("还在吗", "", "chat-uncertain"))
	if secondErr != nil || sender.textCalls != 1 {
		t.Fatalf("不确定结果后不应重发 secondErr=%v calls=%d", secondErr, sender.textCalls)
	}
	// record、recordErr 保存最终隔离状态及查询错误。
	record, recordErr := store.DefaultReps.Record(ctx, "cid", "chat-uncertain")
	if recordErr != nil || record.Status != "uncertain" {
		t.Fatalf("不确定回复未隔离 record=%+v err=%v", record, recordErr)
	}
}

// TestReplyOnceQuarantinesWhenUncertainStateIsRejected 验证 uncertain 状态被数据库约束拒绝时，降级隔离仍阻止租约重发。
func TestReplyOnceQuarantinesWhenUncertainStateIsRejected(t *testing.T) {
	// store、cleanup 保存隔离数据库及其关闭责任。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是默认回复配置、发送状态和租约更新共用的无截止上下文。
	ctx := context.Background()
	// setupErr 保存启用一次性默认回复的配置写入错误。
	if setupErr := store.DefaultReps.Upsert(ctx, "cid", db.DefaultReply{Enabled: true, ReplyOnce: true, ReplyContent: "欢迎"}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// triggerErr 模拟数据库拒绝直接进入 uncertain 状态的约束错误。
	if _, triggerErr := store.DB.ExecContext(ctx, `CREATE TRIGGER deny_uncertain_status BEFORE UPDATE OF status ON default_reply_records WHEN NEW.status='uncertain' BEGIN SELECT RAISE(FAIL,'fixture rejection'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	// sender 让平台返回可能已送达的未知结果。
	sender := &recordingSender{textErr: &ws.SendError{Kind: ws.SendUncertain}}
	// service 使用真实投递记录存储验证降级隔离。
	service := NewReplyService("cid", store, sender, nil, nil, nil)
	// firstErr 保存首次发送返回的不确定错误。
	if firstErr := service.Handle(ctx, chatMsg("你好", "", "chat-uncertain-fallback")); firstErr == nil {
		t.Fatal("不确定发送结果必须返回调用错误")
	}
	// expireErr 保存租约到期模拟更新结果。
	if _, expireErr := store.DB.ExecContext(ctx, `UPDATE default_reply_records SET lease_expires_at=0 WHERE cookie_id=? AND chat_id=?`, "cid", "chat-uncertain-fallback"); expireErr != nil {
		t.Fatal(expireErr)
	}
	// secondErr 保存同一会话再次触发时的结果；降级隔离记录不应再次发送。
	secondErr := service.Handle(ctx, chatMsg("还在吗", "", "chat-uncertain-fallback"))
	if secondErr != nil || sender.textCalls != 1 {
		t.Fatalf("降级隔离后不应重发 secondErr=%v calls=%d", secondErr, sender.textCalls)
	}
	// record、recordErr 保存降级隔离后的记录状态及查询错误。
	record, recordErr := store.DefaultReps.Record(ctx, "cid", "chat-uncertain-fallback")
	if recordErr != nil || record.Status != "pending" {
		t.Fatalf("降级隔离记录状态异常 record=%+v err=%v", record, recordErr)
	}
}

// TestReplyOnceDoesNotReclaimWhenUncertainPersistenceFails 验证未知结果的两次状态写入都失败时仍不会自动重发。
func TestReplyOnceDoesNotReclaimWhenUncertainPersistenceFails(t *testing.T) {
	// store、cleanup 保存隔离数据库及其关闭责任。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是默认回复配置、发送状态和租约更新共用的无截止上下文。
	ctx := context.Background()
	// setupErr 保存启用一次性默认回复的配置写入错误。
	if setupErr := store.DefaultReps.Upsert(ctx, "cid", db.DefaultReply{Enabled: true, ReplyOnce: true, ReplyContent: "欢迎"}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// triggerErr 模拟数据库在未知结果写入期间完全拒绝状态更新。
	if _, triggerErr := store.DB.ExecContext(ctx, `CREATE TRIGGER deny_reply_state_updates BEFORE UPDATE ON default_reply_records BEGIN SELECT RAISE(FAIL,'fixture write failure'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	// sender 模拟平台可能已经送达但连接返回未知结果。
	sender := &recordingSender{textErr: &ws.SendError{Kind: ws.SendUncertain}}
	// service 使用真实投递记录存储验证 sending 状态的持久化保护。
	service := NewReplyService("cid", store, sender, nil, nil, nil)
	// firstErr 保存首次发送返回的不确定错误。
	if firstErr := service.Handle(ctx, chatMsg("你好", "", "chat-uncertain-db-down")); firstErr == nil {
		t.Fatal("不确定发送结果必须返回调用错误")
	}
	// dropErr 恢复租约更新能力，模拟数据库恢复后再次收到同一会话消息。
	if _, dropErr := store.DB.ExecContext(ctx, `DROP TRIGGER deny_reply_state_updates`); dropErr != nil {
		t.Fatal(dropErr)
	}
	// expireErr 模拟原领取租约已过期。
	if _, expireErr := store.DB.ExecContext(ctx, `UPDATE default_reply_records SET lease_expires_at=0 WHERE cookie_id=? AND chat_id=?`, "cid", "chat-uncertain-db-down"); expireErr != nil {
		t.Fatal(expireErr)
	}
	// secondErr 保存数据库恢复后的再次处理结果；sending 记录不得触发第二次外部发送。
	secondErr := service.Handle(ctx, chatMsg("还在吗", "", "chat-uncertain-db-down"))
	if secondErr != nil || sender.textCalls != 1 {
		t.Fatalf("数据库写入失败后不应重发 secondErr=%v calls=%d", secondErr, sender.textCalls)
	}
	// record、recordErr 保存仍需人工核对的发送状态及读取错误。
	record, recordErr := store.DefaultReps.Record(ctx, "cid", "chat-uncertain-db-down")
	if recordErr != nil || record.Status != "sending" {
		t.Fatalf("未知结果状态不应回到可重试 pending record=%+v err=%v", record, recordErr)
	}
}

// TestReply_APIPriorityAndError API 回复命中时优先级最高；API 报错时降级到关键词。
func TestReply_APIPriorityAndError(t *testing.T) {
	// s、cleanup 用于本次流程后续判断的s、cleanup
	s, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 用于本次流程后续判断的ctx
	ctx := context.Background()
	s.DB.ExecContext(ctx, `INSERT INTO keywords (cookie_id,keyword,reply,type) VALUES ('cid','在吗','关键词回复','text')`)

	// API 返回结果 → 用 API。
	api := &fakeAPIReplier{result: &ReplyResult{Text: "API回复"}}
	// r 用于本次流程后续判断的r
	r := NewReplyService("cid", s, nil, api, nil, nil)
	// res 用于本次流程后续判断的响应
	res := r.resolve(ctx, chatMsg("在吗", "", "chat1"))
	if res == nil || res.Source != "API" || res.Text != "API回复" {
		t.Fatalf("API 命中应优先，got %+v", res)
	}

	// API 报错 → 降级到关键词。
	api2 := &fakeAPIReplier{err: errors.New("upstream down")}
	// r2 用于本次流程后续判断的r2
	r2 := NewReplyService("cid", s, nil, api2, nil, nil)
	// res2 用于本次流程后续判断的res2
	res2 := r2.resolve(ctx, chatMsg("在吗", "", "chat1"))
	if res2 == nil || res2.Source != "关键词" || res2.Text != "关键词回复" {
		t.Fatalf("API 报错应降级到关键词，got %+v", res2)
	}

	// API 返回 nil（无回复）→ 降级到关键词。
	api3 := &fakeAPIReplier{result: nil}
	// r3 用于本次流程后续判断的r3
	r3 := NewReplyService("cid", s, nil, api3, nil, nil)
	// res3 用于本次流程后续判断的res3
	res3 := r3.resolve(ctx, chatMsg("在吗", "", "chat1"))
	if res3 == nil || res3.Source != "关键词" {
		t.Fatalf("API nil 应降级到关键词，got %+v", res3)
	}
}

// TestReply_AIPriorityOverDefault AI 回复优先于默认回复；AI 报错降级到默认。
func TestReply_AIPriorityOverDefault(t *testing.T) {
	// s、cleanup 用于本次流程后续判断的s、cleanup
	s, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 用于本次流程后续判断的ctx
	ctx := context.Background()
	s.DB.ExecContext(ctx, `INSERT INTO default_replies (cookie_id,enabled,reply_content,reply_once) VALUES ('cid',1,'默认回复',0)`)

	// ai 用于本次流程后续判断的人工智能
	ai := &fakeAIReplier{result: &ReplyResult{Text: "AI回复"}}
	// r 用于本次流程后续判断的r
	r := NewReplyService("cid", s, nil, nil, ai, nil)
	// res 用于本次流程后续判断的响应
	res := r.resolve(ctx, chatMsg("复杂问题", "", "chat1"))
	if res == nil || res.Source != "AI" || res.Text != "AI回复" {
		t.Fatalf("AI 命中应优先于默认，got %+v", res)
	}

	// AI 报错 → 降级到默认。
	ai2 := &fakeAIReplier{err: errors.New("model timeout")}
	// r2 用于本次流程后续判断的r2
	r2 := NewReplyService("cid", s, nil, nil, ai2, nil)
	// res2 用于本次流程后续判断的res2
	res2 := r2.resolve(ctx, chatMsg("复杂问题", "", "chat1"))
	if res2 == nil || res2.Source != "默认" || res2.Text != "默认回复" {
		t.Fatalf("AI 报错应降级到默认，got %+v", res2)
	}
}

// TestReply_ImageKeyword 图片类型关键词返回 ImageURL。
func TestReply_ImageKeyword(t *testing.T) {
	// s、cleanup 用于本次流程后续判断的s、cleanup
	s, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 用于本次流程后续判断的ctx
	ctx := context.Background()
	s.DB.ExecContext(ctx, `INSERT INTO keywords (cookie_id,keyword,reply,image_url,type) VALUES ('cid','看图','','http://img/x.png','image')`)

	// r 用于本次流程后续判断的r
	r := NewReplyService("cid", s, nil, nil, nil, nil)
	// res 用于本次流程后续判断的响应
	res := r.resolve(ctx, chatMsg("发看图", "item1", "chat1"))
	if res == nil || res.Source != "关键词" || res.ImageURL != "http://img/x.png" {
		t.Fatalf("图片关键词应返回 ImageURL，got %+v", res)
	}
}

// TestReply_HandleSendsImageThenText 验证 Handle 通过完整消息端口先发图片后发文本，且 Skip 不发送；t 管理本测试。
func TestReply_HandleSendsImageThenText(t *testing.T) {
	// s、cleanup 用于本次流程后续判断的s、cleanup
	s, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 用于本次流程后续判断的ctx
	ctx := context.Background()
	s.DB.ExecContext(ctx, `INSERT INTO default_replies (cookie_id,enabled,reply_content,reply_image_url,reply_once) VALUES ('cid',1,'文字','http://img/y.png',0)`)

	// sender 用于本次流程后续判断的sender
	sender := &recordingSender{}
	// r 用于本次流程后续判断的r
	r := NewReplyService("cid", s, sender, nil, nil, nil)
	if // err 用于本次流程后续判断的err
	err := r.Handle(ctx, chatMsg("在吗", "", "chat9")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sender.images) != 1 || sender.images[0].url != "http://img/y.png" || sender.images[0].width != 0 || sender.images[0].height != 0 {
		t.Fatalf("应先发图片，got %+v", sender.images)
	}
	if len(sender.texts) != 1 || sender.texts[0].text != "文字" {
		t.Fatalf("再发文本，got %+v", sender.texts)
	}

	// Skip（空默认回复）不应发送任何内容。
	s.DB.ExecContext(ctx, `UPDATE default_replies SET reply_content='', reply_image_url='' WHERE cookie_id='cid'`)
	// sender2 用于本次流程后续判断的sender2
	sender2 := &recordingSender{}
	// r2 用于本次流程后续判断的r2
	r2 := NewReplyService("cid", s, sender2, nil, nil, nil)
	if // err 用于本次流程后续判断的err
	err := r2.Handle(ctx, chatMsg("在吗", "", "chat9")); err != nil {
		t.Fatalf("Handle skip: %v", err)
	}
	if len(sender2.texts) != 0 || len(sender2.images) != 0 {
		t.Fatalf("Skip 不应发送，got texts=%+v images=%+v", sender2.texts, sender2.images)
	}
}

// TestReply_HandleDelegatesImageURLWithoutDimensionProbe 验证图片 URL 原样交给完整消息端口而不在引擎读取尺寸；t 管理本测试。
func TestReply_HandleDelegatesImageURLWithoutDimensionProbe(t *testing.T) {
	// store 和 cleanup 保存当前测试独占的回复数据库及关闭责任。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// ctx 是当前测试回复读取和投递使用的无截止上下文。
	ctx := context.Background()
	// setupErr 保存图片关键词回复配置写入结果。
	_, setupErr := store.DB.ExecContext(ctx, `INSERT INTO keywords (cookie_id,keyword,reply,image_url,type) VALUES ('cid','照片','','https://images.example/photo.png','image')`)
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	// sender 记录完整消息端口收到的图片地址和尺寸占位。
	sender := &recordingSender{}
	// reply 使用统一完整消息端口发送图片，不配置任何引擎级尺寸解析器。
	reply := NewReplyService("cid", store, sender, nil, nil, nil)
	// sendErr 保存完整消息发送结果。
	if sendErr := reply.Handle(ctx, chatMsg("给我照片", "", "chat-image-dimensions-fallback")); sendErr != nil {
		t.Fatalf("图片回复不应被引擎尺寸解析阻断: %v", sendErr)
	}
	if len(sender.images) != 1 || sender.images[0].width != 0 || sender.images[0].height != 0 {
		t.Fatalf("引擎不应自行设置图片尺寸，实际发送=%+v", sender.images)
	}
}

// TestReply_HandleDelegatesCompleteMessageToChatDelivery 验证自动回复只生成完整消息并交给聊天应用统一发送。
func TestReply_HandleDelegatesCompleteMessageToChatDelivery(t *testing.T) {
	// store、cleanup 保存一次性默认回复状态的隔离数据库。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// setupErr 保存完整图片文字默认回复的配置写入结果。
	if setupErr := store.DefaultReps.Upsert(context.Background(), "cid", db.DefaultReply{Enabled: true, ReplyOnce: true, ReplyContent: "文字", ReplyImageURL: "https://origin.example/reply.jpg"}); setupErr != nil {
		t.Fatal(setupErr)
	}
	// delivery 记录引擎提交的完整消息，并模拟两个分段都已由平台确认。
	delivery := &recordingReplyDelivery{result: ReplySendResult{ImageSent: true, TextSent: true}}
	// reply 使用生产构造路径，不能访问旧版直接发送器或自动回复尺寸探测器。
	reply := NewReplyService("cid", store, delivery, nil, nil, nil)
	// sendErr 保存统一聊天应用发送结果。
	if sendErr := reply.Handle(context.Background(), chatMsg("你好", "", "chat-complete")); sendErr != nil {
		t.Fatalf("Handle: %v", sendErr)
	}
	if len(delivery.messages) != 1 {
		t.Fatalf("完整回复调用次数=%d", len(delivery.messages))
	}
	// message 是交给消息页面的完整图片文字回复，原始 URL 不应携带猜测尺寸。
	message := delivery.messages[0]
	if message.AccountID != "cid" || message.ChatID != "chat-complete" || message.ToUserID != "buyer1" || message.Text != "文字" || message.ImageURL != "https://origin.example/reply.jpg" {
		t.Fatalf("完整回复内容错误=%+v", message)
	}
	// record、recordErr 保存聊天应用成功后的一次性分段状态。
	record, recordErr := store.DefaultReps.Record(context.Background(), "cid", "chat-complete")
	if recordErr != nil || record.Status != "sent" || !record.ImageSent || !record.TextSent {
		t.Fatalf("reply_once 状态错误 record=%+v err=%v", record, recordErr)
	}
}

// TestParseMessageIDFromJSON bizTag/extJson 中提取 messageId。
func TestParseMessageIDFromJSON(t *testing.T) {
	// cases 用于本次流程后续判断的cases
	cases := map[string]string{
		`{"messageId":"abc123"}`: "abc123",
		`{"sourceId":"x"}`:       "",
		`not json`:               "",
		`{}`:                     "",
	}
	// in、want 表示当前遍历过程中的in、want
	for in, want := range cases {
		if // got 用于本次流程后续判断的got
		got := parseMessageIDFromJSON(in); got != want {
			t.Errorf("parseMessageIDFromJSON(%q)=%q want %q", in, got, want)
		}
	}
}

// TestExtractMessageID 优先实时消息的 PNM ID，其次兼容 bizTag/extJson，无则空。
func TestExtractMessageID(t *testing.T) {
	if // got 用于本次流程后续判断的got
	got := extractMessageID(map[string]any{
		"1": map[string]any{
			"3": "4263141580162.PNM",
			"10": map[string]any{
				"bizTag":  `{"messageId":"biz-id"}`,
				"extJson": `{"messageId":"ext-id"}`,
			},
		},
	}); got != "4263141580162.PNM" {
		t.Errorf("PNM 优先: got %q", got)
	}
	if // got 用于本次流程后续判断的got
	got := extractMessageID(map[string]any{
		"1": map[string]any{
			"10": map[string]any{
				"extJson": `{"messageId":"ext-id"}`,
			},
		},
	}); got != "ext-id" {
		t.Errorf("extJson 兜底: got %q", got)
	}
	if // got 用于本次流程后续判断的got
	got := extractMessageID(map[string]any{"1": map[string]any{}}); got != "" {
		t.Errorf("无 ID: got %q", got)
	}
	if // got 用于本次流程后续判断的got
	got := extractMessageID(map[string]any{"1": map[string]any{"10": map[string]any{"extJson": `{"messageId":"legacy-uuid"}`, "nested": map[string]any{"messageId": "4269999999999.PNM"}}}}); got != "4269999999999.PNM" {
		t.Errorf("嵌套 PNM 优先: got %q", got)
	}
	if // got 用于本次流程后续判断的got
	got := extractMessageID(map[string]any{"1": map[string]any{"10": map[string]any{"payload": `{"messageId":"4270000000000.PNM"}`}}}); got != "4270000000000.PNM" {
		t.Errorf("JSON 字符串中的 PNM: got %q", got)
	}
}

// TestMessageContentType extJson 优先，其次 m6.3.4，再其次 m6.3.5 内嵌 JSON。
func TestMessageContentType(t *testing.T) {
	// extJson 命中。
	if got := messageContentType(
		map[string]any{},
		map[string]any{"extJson": `{"contentType":"14"}`},
	); got != "14" {
		t.Errorf("extJson: got %q", got)
	}
	// m6.3.4 数字字段（toString 把 float64 转 "26"）。
	if got := messageContentType(
		map[string]any{"6": map[string]any{"3": map[string]any{"4": float64(26)}}},
		map[string]any{},
	); got != "26" {
		t.Errorf("m6.3.4: got %q", got)
	}
	// m6.3.5 内嵌 JSON。
	if got := messageContentType(
		map[string]any{"6": map[string]any{"3": map[string]any{"5": `{"contentType":"14"}`}}},
		map[string]any{},
	); got != "14" {
		t.Errorf("m6.3.5: got %q", got)
	}
	// 都没有 → 空。
	if got := messageContentType(map[string]any{}, map[string]any{}); got != "" {
		t.Errorf("空: got %q", got)
	}
}

// TestIsNonUserChatNotice contentType 14/26 为系统提示，应过滤。
func TestIsNonUserChatNotice(t *testing.T) {
	if !isNonUserChatNotice(map[string]any{}, map[string]any{"extJson": `{"contentType":"14"}`}, "[提示]") {
		t.Error("contentType=14 应判为系统提示")
	}
	if !isNonUserChatNotice(map[string]any{}, map[string]any{"extJson": `{"contentType":"26"}`}, "[卡片]") {
		t.Error("contentType=26 应判为系统提示")
	}
	if !isNonUserChatNotice(map[string]any{}, map[string]any{"extJson": `{"contentType":"25"}`}, "快给ta一个评价吧～") {
		t.Error("contentType=25 评价提醒应判为系统提示")
	}
	if !isNonUserChatNotice(map[string]any{}, map[string]any{}, "快给ta一个评价吧～") {
		t.Error("评价提醒文案即使缺少扩展字段也不应进入聊天回复")
	}
	if isNonUserChatNotice(map[string]any{}, map[string]any{}, "[买家说你好]") {
		t.Error("普通消息不应判为系统提示")
	}
	if !isNonUserChatNotice(map[string]any{}, map[string]any{"sessionType": "24"}, "售后问卷") {
		t.Error("非真人会话不应进入买家聊天列表")
	}
}

// TestIsNonUserChatNoticeFiltersOfficialSenderAndPlaceholder 封装TestIsNon用户聊天NoticeFiltersOfficialSenderAndPlaceholder业务协调。
func TestIsNonUserChatNoticeFiltersOfficialSenderAndPlaceholder(t *testing.T) {
	if !isNonUserChatNotice(map[string]any{}, map[string]any{"senderUserId": "1400", "reminderContent": "邀您填写售后问卷"}, "邀您填写售后问卷") {
		t.Error("闲小蜜消息应判为官方系统消息")
	}
	if !isNonUserChatNotice(map[string]any{}, map[string]any{"senderUserId": "peer-1"}, "发来一条新消息") {
		t.Error("官方通知占位文本不应进入聊天回复")
	}
}

// TestToStringAndTrimFloatInt 数字/字符串安全转换。
func TestToStringAndTrimFloatInt(t *testing.T) {
	if // got 用于本次流程后续判断的got
	got := toString(float64(26)); got != "26" {
		t.Errorf("toString(float64 26)=%q", got)
	}
	if // got 用于本次流程后续判断的got
	got := toString("hello"); got != "hello" {
		t.Errorf("toString(string)=%q", got)
	}
	if // got 用于本次流程后续判断的got
	got := toString(nil); got != "" {
		t.Errorf("toString(nil)=%q", got)
	}
	if // got 用于本次流程后续判断的got
	got := trimFloatInt(12.00); got != "12" {
		t.Errorf("trimFloatInt(12.00)=%q", got)
	}
	if // got 用于本次流程后续判断的got
	got := trimFloatInt(12.50); got != "12.5" {
		t.Errorf("trimFloatInt(12.50)=%q", got)
	}
}

// TestContains 大小写不敏感包含。
func TestContains(t *testing.T) {
	if !contains("Hello World", "world") {
		t.Error("应大小写不敏感命中")
	}
	if contains("Hello", "xyz") {
		t.Error("不应误命中")
	}
}

// 编译期保证 db 包被引用（测试构造 store 时使用）。
var _ = db.DialectSQLite
