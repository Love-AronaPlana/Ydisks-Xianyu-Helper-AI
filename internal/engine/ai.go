// ai.go AI 回复实现（优先级3）。调用 OpenAI 兼容 chat completions 接口。
// 使用商品信息、对话历史和确定性的价格边界生成回复。

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"

	"xianyu-go/internal/db"
	"xianyu-go/internal/netguard"
)

// defaultAIBaseURL 用于本次流程后续判断的defaultAIBaseURL
const (
	defaultAIBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	defaultAIModel   = "qwen-plus"
)

// newAIHTTPClient 用于本次流程后续判断的newAIHTTPClient
var newAIHTTPClient = func(baseURL string) (*http.Client, error) {
	return netguard.ConfiguredEndpointHTTPClient(baseURL, 30*time.Second)
}

// AIReplierImpl AI 回复实现。
type AIReplierImpl struct {
	cookieID string
	store    *db.Store
	logger   *slog.Logger
}

// NewAIReplier 构造。
func NewAIReplier(cookieID string, store *db.Store, logger *slog.Logger) *AIReplierImpl {
	if logger == nil {
		logger = slog.Default()
	}
	return &AIReplierImpl{
		cookieID: cookieID,
		store:    store,
		logger:   logger.With("account", cookieID, "subsys", "ai"),
	}
}

// Reply 实现 AIReplier 接口。
func (a *AIReplierImpl) Reply(ctx context.Context, m ChatMessage) (*ReplyResult, error) {
	// cfg、err 用于本次流程后续判断的cfg、err
	cfg, err := a.store.AIReply.Get(ctx, a.cookieID)
	if err != nil || cfg == nil || !cfg.AIEnabled {
		return nil, nil // 未启用 AI
	}
	// replyMode 是归一化后的 AI 回复模式；历史未配置或非法值按 bargain 保持旧行为。
	replyMode := cfg.ReplyMode
	if !db.IsAIReplyMode(replyMode) {
		replyMode = db.AIReplyModeBargain
	}
	// isBargain 表示买家消息是否带有砍价意图；完全模式仅对砍价意图执行价格安全拦截。
	isBargain := bargainMessageRe.MatchString(strings.ToLower(m.Text))
	// bargain 模式沿用历史门槛：AI 设置面向砍价场景，普通未命中消息继续交给默认回复，
	// 避免 AI 抢答问候、售后等与砍价无关的消息。
	if replyMode == db.AIReplyModeBargain && !isBargain {
		return nil, nil
	}
	// aiCfg、err 用于本次流程后续判断的人工智能Cfg、err
	aiCfg, err := a.globalAIConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取全局 AI 配置失败: %w", err)
	}
	if aiCfg.APIKey == "" {
		a.logger.Warn("AI 已启用但未配置 APIKey")
		return nil, nil
	}

	// itemContext 保存当前商品的非敏感提示词上下文；商品不存在时使用安全占位信息。
	itemContext := a.promptContext(ctx, m.ItemID)
	// history、bargainCount、err 用于本次流程后续判断的history、bargainCount、err
	history, bargainCount, _, err := a.conversationContext(ctx, m)
	if err != nil {
		return nil, fmt.Errorf("读取 AI 对话历史失败: %w", err)
	}
	if isBargain {
		bargainCount++
	}
	// withinBargainLimit 用于本次流程后续判断的withinBargain上限
	withinBargainLimit := !isBargain || bargainCount <= cfg.MaxBargainRounds
	// systemPrompt 按模式选择基础提示词：bargain 模式沿用砍价提示词，
	// keyword_first/full 模式使用独立的完全模式提示词，砍价意图消息仍追加价格安全规则。
	var systemPrompt string
	if replyMode == db.AIReplyModeBargain {
		systemPrompt = buildSystemPromptWithContext(
			cfg.CustomPrompts, itemContext,
			cfg.MaxDiscountPercent, cfg.MaxDiscountAmount, cfg.MaxBargainRounds, bargainCount, cfg.AutoAdjustPriceEnabled,
		)
	} else {
		systemPrompt = buildFullModeSystemPromptWithContext(cfg.FullPrompt, itemContext)
		if isBargain {
			systemPrompt = appendPriceSafetyRules(systemPrompt, itemContext.price,
				cfg.MaxDiscountPercent, cfg.MaxDiscountAmount, cfg.MaxBargainRounds, bargainCount, cfg.AutoAdjustPriceEnabled)
		}
	}
	if !withinBargainLimit {
		systemPrompt += "\n当前买家已经超过最大砍价轮次。不得继续降价，只能礼貌说明价格不再优惠。"
	}

	// 调 OpenAI 兼容接口。
	clientCfg := openai.DefaultConfig(aiCfg.APIKey)
	if aiCfg.BaseURL != "" {
		clientCfg.BaseURL = aiCfg.BaseURL
	}
	clientCfg.HTTPClient, err = newAIHTTPClient(clientCfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("AI API 地址无效: %w", err)
	}
	// client 用于本次流程后续判断的client
	client := openai.NewClientWithConfig(clientCfg)

	// messages 用于本次流程后续判断的消息列表
	messages := []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: systemPrompt}}
	// message 表示当前遍历过程中的消息
	for _, message := range history {
		// role 用于本次流程后续判断的role
		role := openai.ChatMessageRoleUser
		if message.Role == "assistant" {
			role = openai.ChatMessageRoleAssistant
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: role, Content: truncateAIContent(message.Content)})
	}
	// visionImages 保存成功下载的买家图片；为空表示本条消息退回纯文本请求。
	var visionImages []string
	if cfg.VisionEnabled && len(m.ImageURLs) > 0 {
		visionImages = a.collectAIVisionImages(ctx, m.ImageURLs)
	}
	// userMessage 是当前买家消息；带图时必须使用 MultiContent，go-openai 不允许同时设置 Content。
	userMessage := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: m.Text}
	if len(visionImages) > 0 {
		// parts 是文本与图片组成的多模态正文；首部文本保证纯图片消息仍有可读指令。
		parts := make([]openai.ChatMessagePart, 0, len(visionImages)+1)
		parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: m.Text})
		// dataURI 是当前待附加的买家图片数据；只作为请求体发送，不写日志或数据库。
		for _, dataURI := range visionImages {
			parts = append(parts, openai.ChatMessagePart{
				Type:     openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{URL: dataURI},
			})
		}
		userMessage = openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts}
	}
	messages = append(messages, userMessage)

	// callModel 使用独立的 30 秒预算发起一次对话请求；重试不会复用已被首轮消耗的预算。
	callModel := func(payload []openai.ChatCompletionMessage) (openai.ChatCompletionResponse, error) {
		// aiCtx、cancel 用于本次流程后续判断的人工智能Ctx、cancel
		aiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return client.CreateChatCompletion(aiCtx, openai.ChatCompletionRequest{
			Model:       aiCfg.Model,
			Messages:    payload,
			Temperature: 0.7,
		})
	}
	// resp、err 用于本次流程后续判断的resp、err
	resp, err := callModel(messages)
	if err != nil && len(visionImages) > 0 {
		// 模型不支持视觉输入时带图请求会直接失败；退回纯文本再试一次，避免图片消息完全无法自动回复。
		a.logger.Warn("带图调用失败，退回纯文本重试")
		messages[len(messages)-1] = openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: m.Text}
		resp, err = callModel(messages)
	}
	if err != nil {
		return nil, fmt.Errorf("AI 调用失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, nil
	}
	// reply 用于本次流程后续判断的回复
	reply, markerPrice, markerOK := extractExecutableOffer(strings.TrimSpace(resp.Choices[0].Message.Content))
	if reply == "" {
		return nil, nil
	}
	// minimumPrice 用于本次流程后续判断的minimumPrice
	minimumPrice := minimumAllowedPrice(itemContext.price, cfg.MaxDiscountPercent, cfg.MaxDiscountAmount, withinBargainLimit)
	// quote 是仅在商家开启真实改价且模型结构化报价通过校验时返回的执行提案。
	var quote *AIPriceQuoteProposal
	// 只有砍价意图消息才执行降价拦截与自动改价提案；普通问答只剥离内部标记，
	// 避免日常对话中的正常价格描述被误替换成固定话术。
	if isBargain {
		// markerUnsafe 表示结构化报价突破最低价或高于商品标价，必须和正文越界同样拦截。
		markerUnsafe := markerOK && (markerPrice+0.0001 < minimumPrice || markerPrice > itemContext.price+0.0001)
		if // offered、unsafe 用于本次流程后续判断的offered、unsafe
		offered, unsafe := unsafeOfferedPrice(reply, minimumPrice); unsafe || markerUnsafe {
			if markerUnsafe {
				offered = markerPrice
			}
			a.logger.Warn("AI 报价超过折扣边界，使用安全回复", "offered", offered, "minimum", minimumPrice)
			if minimumPrice >= itemContext.price || !withinBargainLimit {
				reply = "抱歉，当前价格已经是最低价，暂时不能再优惠了。"
			} else {
				reply = fmt.Sprintf("可以优惠的最低价格是 %.2f 元，低于这个价格暂时无法成交。", minimumPrice)
				if cfg.AutoAdjustPriceEnabled {
					quote = &AIPriceQuoteProposal{PriceCents: priceToCents(minimumPrice)}
				}
			}
		} else if cfg.AutoAdjustPriceEnabled && markerOK && markerPrice > 0 && markerPrice+0.0001 < itemContext.price && replyContainsOfferedPrice(reply, markerPrice) {
			quote = &AIPriceQuoteProposal{PriceCents: priceToCents(markerPrice)}
		}
	}
	if m.ChatID != "" && m.ItemID != "" {
		// intent 用于本次流程后续判断的intent
		intent := "chat"
		if isBargain {
			intent = "bargain"
		}
		if // err 用于本次流程后续判断的err
		err := a.store.AIReply.AddConversationExchange(ctx, a.cookieID, m.ChatID, m.SenderUserID, m.ItemID,
			db.AIConversationMessage{Role: "user", Content: m.Text, Intent: intent, BargainCount: bargainCount},
			db.AIConversationMessage{Role: "assistant", Content: reply, Intent: "reply", BargainCount: bargainCount},
		); err != nil {
			return nil, fmt.Errorf("保存 AI 对话失败: %w", err)
		}
	}
	return &ReplyResult{Text: reply, AutoPriceQuote: quote}, nil
}

// conversationContext 封装conversation上下文业务协调。
func (a *AIReplierImpl) conversationContext(ctx context.Context, m ChatMessage) ([]db.AIConversationMessage, int, bool, error) {
	// isBargain 用于本次流程后续判断的isBargain
	isBargain := bargainMessageRe.MatchString(strings.ToLower(m.Text))
	if m.ChatID == "" || m.ItemID == "" {
		return nil, 0, isBargain, nil
	}
	// history、err 用于本次流程后续判断的history、err
	history, err := a.store.AIReply.ConversationHistory(ctx, a.cookieID, m.ChatID, m.ItemID, 10)
	if err != nil {
		return nil, 0, isBargain, err
	}
	// count、err 用于本次流程后续判断的count、err
	count, err := a.store.AIReply.CurrentBargainCount(ctx, a.cookieID, m.ChatID, m.ItemID)
	return history, count, isBargain, err
}

// globalAIConfig 用于本次流程后续判断的globalAI配置
type globalAIConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

// globalAIConfig 封装globalAI配置业务协调。
func (a *AIReplierImpl) globalAIConfig(ctx context.Context) (*globalAIConfig, error) {
	// apiKey、err 用于本次流程后续判断的apiKey、err
	apiKey, err := a.store.ReadSensitiveSettingForAccount(ctx, a.cookieID, "ai_api_key", "settings.use", "ai_reply")
	if err != nil {
		return nil, err
	}
	// baseURL、err 用于本次流程后续判断的baseURL、err
	baseURL, err := a.store.Settings.Get(ctx, "ai_api_url")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL, err = a.store.Settings.Get(ctx, "ai_base_url")
		if err != nil {
			return nil, err
		}
	}
	// model、err 用于本次流程后续判断的model、err
	model, err := a.store.Settings.Get(ctx, "ai_model")
	if err != nil {
		return nil, err
	}

	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultAIBaseURL
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultAIModel
	}
	return &globalAIConfig{
		APIKey:  strings.TrimSpace(apiKey),
		BaseURL: baseURL,
		Model:   model,
	}, nil
}

// itemPromptContext 保存渲染 AI 提示词所需的商品非敏感字段和自定义变量。
type itemPromptContext struct {
	// id 是平台商品标识，缺少商品记录时仍保留消息携带的标识。
	id string
	// title 是商品标题，缺失时使用安全占位文本。
	title string
	// price 是按元计价的商品单价，解析失败时为零。
	price float64
	// description 是商品描述，缺失时回退商品详情或占位文本。
	description string
	// category 是平台商品类目，缺失时使用空字符串。
	category string
	// customVariables 是商品配置提供的自定义变量，只用于本次提示词渲染。
	customVariables map[string]string
	// itemPrompt 是商品级提示词，按商品策略追加或覆盖账号提示词。
	itemPrompt string
	// promptStrategy 是商品级提示词策略，合法值为 inherit、append 或 override。
	promptStrategy string
}

// itemInfo 保留旧测试和调用方需要的商品标题、价格、描述读取接口。
func (a *AIReplierImpl) itemInfo(ctx context.Context, itemID string) (title string, price float64, desc string) {
	// context 是本次商品提示词上下文（标题、价格、描述、类目与商品级提示词），只在本函数内使用。
	context := a.promptContext(ctx, itemID)
	return context.title, context.price, context.description
}

// promptContext 读取商品上下文和商品级提示词；商品提示词读取失败时只记录非敏感诊断并回退账号级提示词。
func (a *AIReplierImpl) promptContext(ctx context.Context, itemID string) itemPromptContext {
	// context 保存即使商品不存在时也可用于渲染的基础商品字段。
	context := itemPromptContext{
		id:              itemID,
		title:           "商品信息获取失败",
		description:     "暂无商品描述",
		customVariables: map[string]string{},
		promptStrategy:  "inherit",
	}
	if strings.TrimSpace(itemID) == "" {
		context.title = "未知商品"
		return context
	}
	// item、itemErr 保存商品读取结果；商品读取失败不阻断 AI，而是继续使用占位上下文。
	item, itemErr := a.store.Items.Get(ctx, a.cookieID, itemID)
	if itemErr == nil && item != nil {
		context.id = item.ItemID
		context.title = item.ItemTitle
		if context.title == "" {
			context.title = "未知商品"
		}
		context.price = parsePrice(item.ItemPrice)
		context.description = item.ItemDescription
		if context.description == "" {
			context.description = item.ItemDetail
		}
		if context.description == "" {
			context.description = "暂无商品描述"
		}
		context.category = item.ItemCategory
	}
	// prompt、configured、promptErr 保存商品级配置、是否已配置及读取错误；日志不携带提示词、变量值或敏感凭证。
	prompt, configured, promptErr := a.store.ItemAIPrompts.Get(ctx, a.cookieID, itemID)
	if promptErr != nil {
		a.logger.Warn("读取商品级 AI 提示词失败，将使用账号级提示词")
	}
	if promptErr == nil && configured {
		context.promptStrategy = prompt.Strategy
		context.itemPrompt = prompt.Prompt
		// variablesErr 仅表示自定义变量 JSON 无法解析；不记录原文，避免日志泄露提示词内容。
		variablesErr := json.Unmarshal([]byte(prompt.CustomVariablesJSON), &context.customVariables)
		if variablesErr != nil {
			context.customVariables = map[string]string{}
			a.logger.Warn("解析商品级 AI 自定义变量失败，将忽略商品变量")
		}
	}
	return context
}

// buildSystemPrompt 构造账号级砍价提示词并保持旧参数接口兼容。
func buildSystemPrompt(customPrompts, itemTitle string, itemPrice float64, itemDesc string, maxDiscountPercent, maxDiscountAmount, maxBargainRounds, bargainCount int, autoAdjustPriceEnabled bool) string {
	// context 保存旧接口提供的商品字段，未配置商品级策略以保持历史行为。
	context := itemPromptContext{title: itemTitle, price: itemPrice, description: itemDesc, promptStrategy: "inherit", customVariables: map[string]string{}}
	return buildSystemPromptWithContext(customPrompts, context, maxDiscountPercent, maxDiscountAmount, maxBargainRounds, bargainCount, autoAdjustPriceEnabled)
}

// buildSystemPromptWithContext 合并账号级和商品级砍价提示词，并在最后追加不可覆盖的价格安全规则。
func buildSystemPromptWithContext(accountPrompt string, context itemPromptContext, maxDiscountPercent, maxDiscountAmount, maxBargainRounds, bargainCount int, autoAdjustPriceEnabled bool) string {
	// base 是策略合并后的业务提示词；安全规则在本函数返回前统一追加。
	base := mergePrompt(accountPrompt, context)
	if strings.TrimSpace(base) == "" {
		base = fmt.Sprintf(`你是闲鱼卖家的自动回复助手。请根据商品信息友好地回复买家。

商品信息：
- 标题：%s
- 价格：%.2f 元
- 描述：%s
- 类目：%s
- 商品 ID：%s

要求：
1. 语气友好自然，像真人卖家
2. 回答简洁，不要过长
3. 不要编造商品没有的功能
4. 直接回复内容，不要加引号或解释`, context.title, context.price, context.description, context.category, context.id)
	}
	return appendPriceSafetyRules(renderPromptVariables(base, context), context.price, maxDiscountPercent, maxDiscountAmount, maxBargainRounds, bargainCount, autoAdjustPriceEnabled)
}

// buildFullModeSystemPrompt 构造完全模式提示词并保持旧参数接口兼容。
func buildFullModeSystemPrompt(fullPrompt, itemTitle string, itemPrice float64, itemDesc string) string {
	// context 是把旧参数接口转换为商品提示词上下文；策略固定 inherit，自定义变量为空映射。
	context := itemPromptContext{title: itemTitle, price: itemPrice, description: itemDesc, promptStrategy: "inherit", customVariables: map[string]string{}}
	return buildFullModeSystemPromptWithContext(fullPrompt, context)
}

// buildFullModeSystemPromptWithContext 构造完全模式提示词；价格安全规则由调用方按砍价消息追加。
func buildFullModeSystemPromptWithContext(accountPrompt string, context itemPromptContext) string {
	// base 是策略合并后的完全模式业务提示词。
	base := mergePrompt(accountPrompt, context)
	if strings.TrimSpace(base) == "" {
		base = fmt.Sprintf(`你是闲鱼卖家的全能自动回复助手，代替卖家回答买家的任何问题。

商品信息：
- 标题：%s
- 价格：%.2f 元
- 描述：%s
- 类目：%s
- 商品 ID：%s

要求：
1. 语气友好自然，像真人卖家
2. 回答简洁准确，不要过长
3. 只根据商品信息和对话内容回答，不要编造商品没有的属性、功能或承诺
4. 涉及发货、售后、退款等需要卖家确认的事项时，礼貌引导买家稍等
5. 直接回复内容，不要加引号或解释`, context.title, context.price, context.description, context.category, context.id)
	}
	return renderPromptVariables(base, context)
}

// mergePrompt 根据 inherit、append、override 策略合并账号和商品提示词；非法策略按 inherit 处理。
func mergePrompt(accountPrompt string, context itemPromptContext) string {
	// itemPrompt 是商品级提示词去除首尾空白后的正文；空串表示商品未配置独立提示词。
	itemPrompt := strings.TrimSpace(context.itemPrompt)
	switch context.promptStrategy {
	case "append":
		if itemPrompt == "" {
			return accountPrompt
		}
		if strings.TrimSpace(accountPrompt) == "" {
			return itemPrompt
		}
		return accountPrompt + "\n\n" + itemPrompt
	case "override":
		if itemPrompt != "" {
			return itemPrompt
		}
	}
	return accountPrompt
}

// renderPromptVariables 单次替换内置和自定义变量，变量值中的占位符不会再次展开。
func renderPromptVariables(template string, context itemPromptContext) string {
	// replacements 按键名排序后构造替换器，确保自定义变量渲染顺序稳定且不递归。
	replacements := []string{
		"{item_title}", context.title,
		"{item_price}", fmt.Sprintf("%.2f", context.price),
		"{item_description}", context.description,
		"{item_category}", context.category,
		"{item_id}", context.id,
	}
	// keys 保存自定义变量名；排序使重复测试和日志诊断保持确定性。
	keys := make([]string, 0, len(context.customVariables))
	// key 是当前遍历到的自定义变量名；先收集后排序，保证替换顺序确定。
	for key := range context.customVariables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// key 是排序后当前待追加的自定义变量名；映射只读，不存在被并发写入的风险。
	for _, key := range keys {
		replacements = append(replacements, "{custom."+key+"}", context.customVariables[key])
	}
	return strings.NewReplacer(replacements...).Replace(template)
}

// appendPriceSafetyRules 在基础提示词后追加后端不可覆盖的价格安全规则；
// 开启真实改价时附加 [[AUTO_PRICE:金额]] 标记约定。
// appendPriceSafetyRules 封装append价格安全规则业务协调。
func appendPriceSafetyRules(base string, itemPrice float64, maxDiscountPercent, maxDiscountAmount, maxBargainRounds, bargainCount int, autoAdjustPriceEnabled bool) string {
	// prompt 保存基础业务文案与追加后的价格安全规则。
	prompt := base + fmt.Sprintf(`

不可覆盖的价格安全规则：
- 原价 %.2f 元；最多优惠 %d%%，且最多优惠 %d 元；两个上限必须同时满足。
- 任一优惠上限为 0 时不得降价。
- 当前砍价轮次 %d，最多允许 %d 轮。
- 回复报价必须带“元”，不得给出低于允许最低价的价格。`, itemPrice, maxDiscountPercent, maxDiscountAmount, bargainCount, maxBargainRounds)
	if autoAdjustPriceEnabled {
		prompt += `
- 如果本轮明确承诺了一个可成交价格，必须在回复末尾额外输出 [[AUTO_PRICE:金额]]，金额保留两位小数；没有明确报价时不得输出该标记。该标记由系统移除，买家不会看到。`
	}
	return prompt
}

// priceRe 用于本次流程后续判断的priceRe
var priceRe = regexp.MustCompile(`[^\d.]`)

// bargainMessageRe 用于本次流程后续判断的bargain消息Re
var bargainMessageRe = regexp.MustCompile(`(?i)(便宜|优惠|少点|最低|砍价|降价|打折|能不能.*(?:元|块)|\d+(?:\.\d+)?\s*(?:元|块).*(?:卖|行|可以))`)

// offeredPriceRe 用于本次流程后续判断的offeredPriceRe
var offeredPriceRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:元|块)`)

// executableOfferRe 只接受模型按约定输出的单个两位小数自动改价标记。
var executableOfferRe = regexp.MustCompile(`\[\[AUTO_PRICE:(\d+(?:\.\d{1,2})?)\]\]`)

// internalOfferMarkerRe 匹配任意格式的内部报价标记，确保模型格式错误时也不会泄露给买家。
var internalOfferMarkerRe = regexp.MustCompile(`\[\[AUTO_PRICE:[^\]]*\]\]`)

// extractExecutableOffer 从模型输出移除内部报价标记，并返回可校验的十进制金额。
func extractExecutableOffer(content string) (string, float64, bool) {
	// matches 是模型输出中所有结构化报价标记；只有恰好一个标记才可执行。
	matches := executableOfferRe.FindAllStringSubmatch(content, -1)
	// visible 是删除所有内部标记后真正发送给买家的文本。
	visible := strings.TrimSpace(internalOfferMarkerRe.ReplaceAllString(content, ""))
	if len(matches) != 1 {
		return visible, 0, false
	}
	// price 是标记中的元金额；err 表示模型输出无法解析为有限十进制数。
	price, err := strconv.ParseFloat(matches[0][1], 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) {
		return visible, 0, false
	}
	return visible, price, true
}

// priceToCents 把已经通过边界校验的元金额四舍五入为整数分。
func priceToCents(price float64) int64 {
	return int64(math.Round(price * 100))
}

// replyContainsOfferedPrice 判断内部执行价格是否与买家可见正文中的元/块报价一致。
func replyContainsOfferedPrice(reply string, target float64) bool {
	// match 是正文中当前待比较的显式价格匹配结果。
	for _, match := range offeredPriceRe.FindAllStringSubmatch(reply, -1) {
		// price 是买家可见的报价金额；err 表示该匹配无法解析为数值。
		price, err := strconv.ParseFloat(match[1], 64)
		if err == nil && math.Abs(price-target) < 0.0001 {
			return true
		}
	}
	return false
}

// minimumAllowedPrice 封装minimumAllowedPrice业务协调。
func minimumAllowedPrice(price float64, maxDiscountPercent, maxDiscountAmount int, allowDiscount bool) float64 {
	if price <= 0 {
		return 0
	}
	if !allowDiscount || maxDiscountPercent <= 0 || maxDiscountAmount <= 0 {
		return price
	}
	// byPercent 用于本次流程后续判断的byPercent
	byPercent := price * (1 - float64(maxDiscountPercent)/100)
	// byAmount 用于本次流程后续判断的byAmount
	byAmount := price - float64(maxDiscountAmount)
	// minimum 是两个折扣边界中更严格的原始最低价。
	minimum := math.Max(0, math.Max(byPercent, byAmount))
	// 金额向上取整到分，避免显示或执行价格因普通四舍五入突破折扣上限。
	return math.Ceil(minimum*100-0.0000001) / 100
}

// unsafeOfferedPrice 封装unsafeOfferedPrice业务协调。
func unsafeOfferedPrice(reply string, minimum float64) (float64, bool) {
	if minimum <= 0 {
		return 0, false
	}
	// match 表示当前遍历过程中的match
	for _, match := range offeredPriceRe.FindAllStringSubmatch(reply, -1) {
		// value、err 用于本次流程后续判断的value、err
		value, err := strconv.ParseFloat(match[1], 64)
		if err == nil && value+0.0001 < minimum {
			return value, true
		}
	}
	return 0, false
}

// truncateAIContent 封装truncateAI内容业务协调。
func truncateAIContent(content string) string {
	// maxRunes 用于本次流程后续判断的maxRunes
	const maxRunes = 2000
	// runes 用于本次流程后续判断的runes
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return string(runes[:maxRunes])
}

// parsePrice 移除非数字字符后转换为 float。
func parsePrice(s string) float64 {
	// cleaned 用于本次流程后续判断的cleaned
	cleaned := priceRe.ReplaceAllString(s, "")
	if cleaned == "" {
		return 0
	}
	// f 用于本次流程后续判断的f
	f, _ := strconv.ParseFloat(cleaned, 64)
	return f
}
