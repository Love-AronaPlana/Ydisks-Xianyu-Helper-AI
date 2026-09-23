// reply.go 四级回复引擎：API → 关键词 → AI → 默认回复。
// 实现关键词回复、默认回复和 AI 回复的调度。
//
// Phase 3 实现：关键词（含商品ID优先+变量替换+空回复标记）、默认回复（指定商品优先+reply_once+变量替换）。
// API 回复（调外部 /xianyu/reply 接口）和 AI 回复（OpenAI 兼容）留接口注入。

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/ws"
)

// ReplyResult 回复结果。
type ReplyResult struct {
	Text      string // 文本回复（可空）
	ImageURL  string // 图片回复（可空）
	Source    string // 回复来源：API/关键词/AI/默认
	Skip      bool   // true 表示匹配到空回复，不发送任何内容
	ReplyOnce bool   // 仅默认回复使用，发送状态由 Handle 持久化
	// AutoPriceQuote 是 AI 已明确承诺且通过价格边界校验的可执行报价。
	AutoPriceQuote *AIPriceQuoteProposal
}

// AIPriceQuoteProposal 是等待回复发送成功后绑定到会话的 AI 报价。
type AIPriceQuoteProposal struct {
	// PriceCents 是以分为单位的单件成交报价；订单执行时按已知购买数量折算总价。
	PriceCents int64
}

// aiQuoteValidity 是 AI 报价从成功发送开始允许自动应用到新订单的时长。
const aiQuoteValidity = 30 * time.Minute

// replyRecordPersistTimeout 是发送结果不确定时写入隔离状态允许使用的最长时间。
const replyRecordPersistTimeout = 5 * time.Second

// APIReplier 外部 API 回复（优先级1）。返回 nil 表示无回复。
type APIReplier interface {
	Reply(ctx context.Context, m ChatMessage) (*ReplyResult, error)
}

// AIReplier AI 回复（优先级3）。返回 nil 表示无回复。
type AIReplier interface {
	Reply(ctx context.Context, m ChatMessage) (*ReplyResult, error)
}

// ReplyMessage 是引擎生成并交给聊天应用的一条完整回复消息。
type ReplyMessage struct {
	// AccountID 是回复所属账号标识。
	AccountID string
	// ChatID 是目标聊天会话标识。
	ChatID string
	// ToUserID 是回复接收方的平台用户标识。
	ToUserID string
	// Text 是可选的文字回复。
	Text string
	// ImageURL 是可选的来源图片地址；上传和尺寸处理由聊天应用完成。
	ImageURL string
}

// ReplySendResult 是聊天应用投递完整回复后的分段确认结果。
type ReplySendResult struct {
	// ImageSent 表示图片已由平台确认接收。
	ImageSent bool
	// TextSent 表示文字已由平台确认接收。
	TextSent bool
	// Uncertain 表示平台动作可能已经发生，调用方不得安全重试。
	Uncertain bool
}

// ReplyDelivery 是回复服务使用的完整消息投递端口。
type ReplyDelivery interface {
	// SendReply 发送完整回复；实现方负责复用消息页面的图片上传和文字发送规则。
	SendReply(ctx context.Context, message ReplyMessage) (ReplySendResult, error)
}

// ReplyService 单账号回复服务。
type ReplyService struct {
	cookieID string
	store    *db.Store
	api      APIReplier // 可为 nil
	ai       AIReplier  // 可为 nil
	// delivery 是生产自动回复使用的聊天应用完整消息发送端口。
	delivery ReplyDelivery
	logger   *slog.Logger
	// now 提供当前时间；生产环境使用系统时钟，测试可注入确定性的过期边界。
	now func() time.Time
}

// NewReplyService 构造。
func NewReplyService(cookieID string, store *db.Store, delivery ReplyDelivery,
	api APIReplier, ai AIReplier, logger *slog.Logger) *ReplyService {
	return newReplyService(cookieID, store, delivery, api, ai, logger)
}

// newReplyService 统一构造回复解析和完整消息投递依赖。
func newReplyService(cookieID string, store *db.Store, delivery ReplyDelivery, api APIReplier, ai AIReplier, logger *slog.Logger) *ReplyService {
	if logger == nil {
		logger = slog.Default()
	}
	// service 保存统一装配的回复解析、状态存储和消息投递端口。
	service := &ReplyService{
		cookieID: cookieID,
		store:    store,
		api:      api,
		ai:       ai,
		delivery: delivery,
		logger:   logger.With("account", cookieID, "subsys", "reply"),
		now:      time.Now,
	}
	return service
}

// Handle 收到一条聊天消息，按四级优先级回复。
// 由 Account 在防抖后调用。返回是否产生了回复。
// Handle 处理当前值。
func (r *ReplyService) Handle(ctx context.Context, m ChatMessage) error {
	// res 用于本次流程后续判断的响应
	res := r.resolve(ctx, m)
	if res == nil || res.Skip {
		return nil
	}
	// 发送：图片优先，文本随后。reply_once 使用持久化分段状态，失败时只重试
	// 尚未成功的部分。
	if r.delivery == nil {
		return nil
	}
	// record 用于本次流程后续判断的record
	record := db.DefaultReplyRecord{}
	if res.ReplyOnce && m.ChatID != "" {
		// claimed 用于本次流程后续判断的claimed
		var claimed bool
		// err 用于本次流程后续判断的err
		var err error
		record, claimed, err = r.store.DefaultReps.ClaimRecord(ctx, r.cookieID, m.ChatID, res.Text != "", res.ImageURL != "")
		if err != nil {
			return fmt.Errorf("领取默认回复发送任务: %w", err)
		}
		if !claimed {
			return nil
		}
	}
	return r.handleWithDelivery(ctx, m, res, record)
}

// handleWithDelivery 把完整回复交给聊天应用，并把其分段结果同步到 reply_once 状态。
func (r *ReplyService) handleWithDelivery(ctx context.Context, m ChatMessage, res *ReplyResult, record db.DefaultReplyRecord) error {
	// message 保存本次仍需发送的完整回复；已成功的 reply_once 分段会被剔除。
	message := ReplyMessage{AccountID: r.cookieID, ChatID: m.ChatID, ToUserID: m.SenderUserID, Text: res.Text, ImageURL: res.ImageURL}
	if record.ImageSent {
		message.ImageURL = ""
	}
	if record.TextSent {
		message.Text = ""
	}
	if strings.TrimSpace(message.Text) == "" && strings.TrimSpace(message.ImageURL) == "" {
		return nil
	}
	// sent、sendErr 保存消息页面完整发送入口返回的分段确认和错误。
	sent, sendErr := r.delivery.SendReply(ctx, message)
	if sent.ImageSent && !record.ImageSent && res.ReplyOnce && m.ChatID != "" {
		// markErr 保存图片分段状态持久化结果。
		if markErr := r.store.DefaultReps.MarkPartSent(ctx, r.cookieID, m.ChatID, "image"); markErr != nil {
			r.markReplyUncertain(ctx, res, m, markErr)
			return markErr
		}
	}
	if sent.TextSent && !record.TextSent && res.ReplyOnce && m.ChatID != "" {
		// markErr 保存文字分段状态持久化结果。
		if markErr := r.store.DefaultReps.MarkPartSent(ctx, r.cookieID, m.ChatID, "text"); markErr != nil {
			r.markReplyUncertain(ctx, res, m, markErr)
			return markErr
		}
	}
	if sendErr != nil {
		r.logger.Error("通过聊天应用发送回复失败", "err", sendErr)
		if sent.Uncertain {
			r.markReplyUncertain(ctx, res, m, sendErr)
			return sendErr
		}
		// persistErr 保存确定未发送状态写入错误。
		if persistErr := r.markReplyFailure(ctx, res, m, sendErr); persistErr != nil {
			return errors.Join(sendErr, persistErr)
		}
		return sendErr
	}
	if sent.TextSent && res.Source == "AI" && res.AutoPriceQuote != nil && m.ChatID != "" && m.SenderUserID != "" && m.ItemID != "" {
		// quote 保存完整回复文字已确认后才允许执行的 AI 报价。
		quote := db.AIBargainQuote{CookieID: r.cookieID, ChatID: m.ChatID, BuyerID: m.SenderUserID, ItemID: m.ItemID, PriceCents: res.AutoPriceQuote.PriceCents}
		// expiresAt 保存固定 30 分钟报价有效期的 Unix 秒时间。
		expiresAt := time.Now().UTC().Add(aiQuoteValidity).Unix()
		// quoteErr 保存已发送报价写入失败原因。
		if quoteErr := r.store.AIReply.ReplacePendingQuote(ctx, quote, expiresAt); quoteErr != nil {
			return fmt.Errorf("保存已发送的 AI 自动改价报价: %w", quoteErr)
		}
	}
	if res.ReplyOnce && m.ChatID != "" {
		// recordErr 保存完整回复记录收口结果。
		if recordErr := r.store.DefaultReps.MarkRecordSent(ctx, r.cookieID, m.ChatID); recordErr != nil {
			r.markReplyUncertain(ctx, res, m, recordErr)
			return recordErr
		}
	}
	return nil
}

// markReplyFailure 持久化确定未发送的默认回复失败状态，并反馈状态写入错误。
func (r *ReplyService) markReplyFailure(ctx context.Context, res *ReplyResult, m ChatMessage, sendErr error) error {
	if res.ReplyOnce && m.ChatID != "" {
		// transportErr 只接受聊天传输层明确声明的未知发送结果；普通本地错误仍允许重试。
		var transportErr *ws.SendError
		if errors.As(sendErr, &transportErr) && ws.SendResultKind(sendErr) == ws.SendUncertain {
			r.markReplyUncertain(ctx, res, m, sendErr)
			return nil
		}
		// persistCtx 使用独立的短超时，保证发送方取消上下文不会遗留不可领取的 sending 状态。
		persistCtx, cancel := context.WithTimeout(context.Background(), replyRecordPersistTimeout)
		defer cancel()
		// persistErr 保存确定未发送状态写入结果。
		if persistErr := r.store.DefaultReps.MarkRecordFailed(persistCtx, r.cookieID, m.ChatID, sendErr.Error()); persistErr != nil {
			return fmt.Errorf("保存默认回复失败状态: %w", persistErr)
		}
	}
	return nil
}

// markReplyUncertain 在消息已经可能送达后本地状态写入失败时隔离一次性默认回复。
func (r *ReplyService) markReplyUncertain(ctx context.Context, res *ReplyResult, m ChatMessage, cause error) {
	if res.ReplyOnce && m.ChatID != "" {
		// persistCtx 让取消的发送上下文不会阻止未知结果进入隔离状态。
		persistCtx, cancel := context.WithTimeout(context.Background(), replyRecordPersistTimeout)
		defer cancel()
		// persistErr 保存未知投递结果隔离失败，便于运维发现无法持久化的人工核对状态。
		if persistErr := r.store.DefaultReps.MarkRecordUncertain(persistCtx, r.cookieID, m.ChatID, cause.Error()); persistErr != nil {
			r.logger.Error("隔离未知默认回复结果失败", "err", persistErr)
		}
	}
}

// resolve 按优先级确定回复内容（不发送）。
func (r *ReplyService) resolve(ctx context.Context, m ChatMessage) *ReplyResult {
	// handoffBlocksAI 表示人工接管已生效或接管状态无法可靠读取；两种情况都不得调用 AI。
	handoffBlocksAI := r.humanHandoffBlocksAI(ctx, m)

	// 优先级1：API 回复。
	if r.api != nil {
		if // res、err 用于本次流程后续判断的res、err
		res, err := r.api.Reply(ctx, m); err != nil {
			r.logger.Error("API 回复失败", "err", err)
		} else if res != nil {
			res.Source = "API"
			return res
		}
	}

	// 优先级2：关键词匹配。AI 完全接管模式下跳过，所有买家消息直接由 AI 回答；
	// 模式读取失败时按未接管处理，保持既有回复链不受影响。
	if !r.aiTakesOverAll(ctx) {
		// res 是关键词规则命中的回复结果；空值表示继续进入 AI 或默认回复。
		if res := r.keywordReply(ctx, m); res != nil {
			return res
		}
	}

	// 优先级3：AI 回复；人工接管期间继续进入默认回复，不调用 AI。
	if r.ai != nil && !handoffBlocksAI {
		if // res、err 用于本次流程后续判断的res、err
		res, err := r.ai.Reply(ctx, m); err != nil {
			r.logger.Error("AI 回复失败", "err", err)
		} else if res != nil {
			res.Source = "AI"
			return res
		}
	}

	// 优先级4：默认回复。
	return r.defaultReply(ctx, m)
}

// NormalizeHumanHandoffBuyerID 把平台发送者标识归一为人工接管隔离键。
// 入站消息有时带 “@goofish” 后缀、有时不带，会话页面对端标识与消息发送者必须落到同一个键，
// 否则同一个买家会被拆成两个隔离目标，出现接管不生效或无法提前结束的情况。
func NormalizeHumanHandoffBuyerID(raw string) string {
	return strings.TrimSuffix(strings.TrimSpace(raw), "@goofish")
}

// humanHandoffBlocksAI 处理人工关键词的接管激活和当前买家状态查询。
// 接管仅按账号与买家标识隔离；状态读写失败时阻止 AI，且日志不记录消息正文。
func (r *ReplyService) humanHandoffBlocksAI(ctx context.Context, m ChatMessage) bool {
	// now 返回当前 Unix 时间；注入时钟让过期边界测试不依赖真实时间流逝。
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	// nowUnix 是本次判定使用的时间基准，供激活窗口与到期判定共用。
	nowUnix := now().UTC().Unix()
	// buyerKey 是人工接管的隔离键；空值表示本条消息无法确定买家身份。
	buyerKey := NormalizeHumanHandoffBuyerID(m.SenderUserID)
	// hasHandoffKeyword 表示当前消息是否要求人工接管；只检查固定业务关键词，不记录正文。
	hasHandoffKeyword := strings.Contains(m.Text, "人工")
	if hasHandoffKeyword {
		if r.store == nil || r.store.AIReply == nil {
			r.logger.Error("人工接管依赖未初始化")
			return r.ai != nil
		}
		// settings 保存账号 AI 设置；settingsErr 表示读取设置失败，失败时必须阻止 AI。
		settings, settingsErr := r.store.AIReply.Get(ctx, r.cookieID)
		if settingsErr != nil {
			r.logger.Error("读取人工接管配置失败", "err", settingsErr)
			return r.ai != nil
		}
		if settings != nil && settings.HumanHandoffMinutes > 0 {
			if buyerKey == "" {
				r.logger.Error("人工接管消息缺少买家标识")
				return r.ai != nil
			}
			// until 是人工接管截止时间的 Unix 秒；分钟值来自账号设置并按秒换算。
			until := now().UTC().Add(time.Duration(settings.HumanHandoffMinutes) * time.Minute).Unix()
			// activateErr 是写入人工接管记录失败的错误；失败时仅记录日志并继续，不影响后续 API 与关键词回复。
			if activateErr := r.store.AIReply.ActivateHumanHandoff(ctx, r.cookieID, buyerKey, until); activateErr != nil {
				r.logger.Error("激活人工接管失败", "err", activateErr)
				return r.ai != nil
			}
			// 记录接管窗口的截止时刻，便于运营在日志中确认 AI 何时恢复（不记录买家消息正文）。
			r.logger.Info("人工接管已生效", "buyer", buyerKey,
				"minutes", settings.HumanHandoffMinutes, "until", time.Unix(until, 0).Format(time.RFC3339))
			// 当前触发消息也不能调用 AI；API 和关键词优先级仍在本函数返回后继续执行。
			return true
		}
	}
	if buyerKey == "" {
		return false
	}
	if r.store == nil || r.store.AIReply == nil {
		r.logger.Error("人工接管依赖未初始化")
		return r.ai != nil
	}
	// pausedUntil、found、readErr 保存当前账号与买家的接管截止时间、存在状态与读取错误。
	pausedUntil, found, readErr := r.store.AIReply.GetHumanHandoff(ctx, r.cookieID, buyerKey)
	if readErr != nil {
		// 数据库读取失败必须按接管处理，宁可少回也不在人工接管期间抢答。
		r.logger.Error("读取人工接管状态失败", "err", readErr)
		return r.ai != nil
	}
	if !found {
		return false
	}
	if pausedUntil > nowUnix {
		return true
	}
	// 已到期：清理记录并记录一次恢复日志，既避免过期记录无限累积，也让运营能直接看到 AI 恢复时刻。
	if _, clearErr := r.store.AIReply.ClearHumanHandoff(ctx, r.cookieID, buyerKey); clearErr != nil {
		r.logger.Warn("清理过期人工接管记录失败", "buyer", buyerKey, "err", clearErr)
	}
	r.logger.Info("人工接管已到期，恢复 AI 回复", "buyer", buyerKey,
		"expired_at", time.Unix(pausedUntil, 0).Format(time.RFC3339))
	return false
}

// aiTakesOverAll 判断 AI 是否配置为完全接管模式（full）。
// 未注入 AI 或读取失败时按未接管处理；AI 失败或返回空时仍回退到默认回复链。
func (r *ReplyService) aiTakesOverAll(ctx context.Context) bool {
	if r.ai == nil {
		return false
	}
	// mode 是账号当前配置的 AI 回复模式；err 是只读模式列的查询错误。
	mode, err := r.store.AIReply.ReplyMode(ctx, r.cookieID)
	if err != nil {
		r.logger.Error("读取 AI 回复模式失败", "err", err)
		return false
	}
	if mode != db.AIReplyModeFull {
		return false
	}
	// enabled 表示 AI 实际是否启用；AI 未启用时不能屏蔽关键词规则。
	enabled, err := r.store.AIReply.IsEnabled(ctx, r.cookieID)
	if err != nil {
		r.logger.Error("读取 AI 回复开关失败", "err", err)
		return false
	}
	return enabled
}

// keywordReply 关键词匹配。返回 nil 表示无匹配；
// 返回 Skip=true 表示匹配到空回复（不发送）。
// 移植自 get_keyword_reply：商品ID关键词优先 → 通用关键词。
// keywordReply 封装关键词回复业务协调。
func (r *ReplyService) keywordReply(ctx context.Context, m ChatMessage) *ReplyResult {
	// kws、err 用于本次流程后续判断的kws、err
	kws, err := r.store.Keywords.AllWithType(ctx, r.cookieID)
	if err != nil || len(kws) == 0 {
		return nil
	}
	// msgLower 用于本次流程后续判断的msgLower
	msgLower := strings.ToLower(m.Text)

	// 1. 商品ID关键词优先；一条规则可关联多个商品，命中任一即可。
	if m.ItemID != "" {
		// kw 表示当前遍历过程中的kw
		for _, kw := range kws {
			if containsItemID(kw.ItemID, m.ItemID) && strings.Contains(msgLower, strings.ToLower(kw.Keyword)) {
				return r.keywordResult(kw, m)
			}
		}
	}
	// 2. 通用关键词（无 item_id）。
	for _, kw := range kws {
		if kw.ItemID == "" && strings.Contains(msgLower, strings.ToLower(kw.Keyword)) {
			return r.keywordResult(kw, m)
		}
	}
	return nil
}

// containsItemID 判断规则的商品范围字段是否覆盖目标商品标识。
// 字段为逗号分隔的商品集合时命中任一元素即视为覆盖；空字段不会匹配任何商品。
// 引擎直接读取持久化字段，故分隔符必须与关键词应用服务的存储约定保持一致。
func containsItemID(rawItemIDs string, target string) bool {
	// itemID 表示规则商品范围中的单个商品标识。
	for _, itemID := range strings.Split(rawItemIDs, ",") {
		if strings.TrimSpace(itemID) == target {
			return true
		}
	}
	return false
}

// keywordResult 封装关键词结果业务协调。
func (r *ReplyService) keywordResult(kw db.Keyword, m ChatMessage) *ReplyResult {
	if kw.Type == "image" && kw.ImageURL != "" {
		return &ReplyResult{ImageURL: kw.ImageURL, Source: "关键词"}
	}
	if strings.TrimSpace(kw.Reply) == "" {
		return &ReplyResult{Skip: true, Source: "关键词"} // EMPTY_REPLY
	}
	return &ReplyResult{Text: formatReply(kw.Reply, m), Source: "关键词"}
}

// defaultReply 默认回复。移植自 get_default_reply：
// 指定商品回复优先 → 账号默认回复（reply_once 防重复 + 变量替换）。
// defaultReply 封装default回复业务协调。
func (r *ReplyService) defaultReply(ctx context.Context, m ChatMessage) *ReplyResult {
	// 1. 指定商品回复。
	if m.ItemID != "" {
		if // ir、err 用于本次流程后续判断的ir、err
		ir, err := r.store.ItemReps.Get(ctx, r.cookieID, m.ItemID); err == nil && ir != nil && strings.TrimSpace(ir.ReplyContent) != "" {
			return &ReplyResult{Text: formatReplyWithItem(ir.ReplyContent, m), Source: "默认"}
		}
	}
	// 2. 账号默认回复。
	dr, err := r.store.DefaultReps.Get(ctx, r.cookieID)
	if err != nil || dr == nil || !dr.Enabled {
		return nil
	}
	// 文字和图片都为空 → 空回复标记。
	if strings.TrimSpace(dr.ReplyContent) == "" && strings.TrimSpace(dr.ReplyImageURL) == "" {
		return &ReplyResult{Skip: true, Source: "默认"}
	}
	// res 用于本次流程后续判断的响应
	res := &ReplyResult{Source: "默认", ReplyOnce: dr.ReplyOnce}
	if strings.TrimSpace(dr.ReplyContent) != "" {
		res.Text = formatReply(dr.ReplyContent, m)
	}
	if strings.TrimSpace(dr.ReplyImageURL) != "" {
		res.ImageURL = dr.ReplyImageURL
	}
	return res
}

// formatReply 变量替换：{send_user_name} {send_user_id} {send_message}。
// 替换回复模板变量；若替换出错则返回原文。
// formatReply 封装format回复业务协调。
func formatReply(template string, m ChatMessage) string {
	return safeFormat(template, map[string]string{
		"send_user_name": m.SenderName,
		"send_user_id":   m.SenderUserID,
		"send_message":   m.Text,
	})
}

// formatReplyWithItem 含 {item_id} 变量。
func formatReplyWithItem(template string, m ChatMessage) string {
	return safeFormat(template, map[string]string{
		"send_user_name": m.SenderName,
		"send_user_id":   m.SenderUserID,
		"send_message":   m.Text,
		"item_id":        m.ItemID,
	})
}

// safeFormat 实现命名占位符替换。
func safeFormat(template string, vars map[string]string) string {
	// out 用于本次流程后续判断的out
	out := template
	// k、v 表示当前遍历过程中的k、v
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}
