package automation

import (
	"context"
	"testing"

	"xianyu-go/internal/db"
)

// seedAccountWidePaidRule 写入一条账号级付款发货规则，并按需要开启显式 allow_all_items 授权。
// 返回规则标识；accountWide 为真时表示用户已确认该规则可作用于账号下全部商品与规格。
func seedAccountWidePaidRule(t *testing.T, store *db.Store, cardID int64, name string, accountWide bool) int64 {
	t.Helper()
	// ctx 是规则夹具写入使用的上下文。
	ctx := context.Background()
	// admin 是规则所属的管理员用户。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// configJSON 是账号级授权标记的规则配置；未授权时保持空对象，确保不会被误认为允许全部商品。
	configJSON := `{}`
	if accountWide {
		configJSON = `{"allow_all_items":true}`
	}
	// ruleID、createErr 保存新建规则的标识与失败原因。
	ruleID, createErr := store.Automation.Create(ctx, db.AutomationRuleInput{
		UserID: admin.ID, CookieID: "cid", ItemID: "", Name: name, TriggerType: TriggerOrderPaid, Enabled: true,
		ConfigJSON: configJSON,
		Actions: []db.AutomationActionInput{
			{ActionType: ActionSendCard, CardID: cardID, DeliveryCount: 1, ConfigJSON: `{}`, Enabled: true, SortOrder: 1},
		},
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	return ruleID
}

// newAccountWideDeliveryFixture 构造账号级付款规则的发送夹具。
// accountWide 控制规则是否显式授权忽略订单规格；返回上下文、存储、中心、发送记录、卡密组标识与释放函数。
func newAccountWideDeliveryFixture(t *testing.T, accountWide bool) (context.Context, *db.Store, *Center, *testSender, func()) {
	t.Helper()
	// store、cleanup 保存测试数据库及其关闭函数。
	store, cleanup := newAutomationTestStore(t)
	// ctx 是夹具共用的数据库上下文。
	ctx := context.Background()
	// admin 是创建卡密所需的管理员用户。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// cardID、cardErr 保存测试卡密组标识与创建失败原因。
	cardID, cardErr := store.Cards.Create(ctx, &db.CardFull{
		Name: "account-wide-card", Type: "text", TextContent: "ACCOUNT-WIDE-CARD", Enabled: true, UserID: admin.ID,
	})
	if cardErr != nil {
		t.Fatal(cardErr)
	}
	seedAccountWidePaidRule(t, store, cardID, "account-wide-paid", accountWide)
	// sender 记录自动化发送的卡密消息。
	sender := &testSender{}
	// center 是待验证的自动化中心，不注入远端改价与详情依赖。
	center := NewWithDependencies(store, testSenderProvider{sender: sender}, nil, CenterDependencies{})
	return ctx, store, center, sender, cleanup
}

// TestRuleAllowsAllItemsRequiresExplicitAccountPaidConfirmation 验证账号级规格授权只在显式开启时成立。
func TestRuleAllowsAllItemsRequiresExplicitAccountPaidConfirmation(t *testing.T) {
	// 只有 order_paid 触发类型 + 无商品绑定 + allow_all_items=true 才授权忽略规格。
	authorized := db.AutomationRule{ItemID: "", TriggerType: TriggerOrderPaid, ConfigJSON: `{"allow_all_items":true}`}
	if !ruleAllowsAllItems(authorized, TriggerOrderPaid) {
		t.Fatal("显式账号级授权应被识别")
	}
	// 缺少授权字段、绑定商品、触发类型不符或配置非法时必须拒绝。
	rejected := []struct {
		// name 是该用例的测试名称。
		name string
		// rule 是待判定的规则。
		rule db.AutomationRule
		// triggerType 是本次事件的触发类型。
		triggerType string
	}{
		{name: "missing-flag", rule: db.AutomationRule{TriggerType: TriggerOrderPaid, ConfigJSON: `{}`}, triggerType: TriggerOrderPaid},
		{name: "flag-false", rule: db.AutomationRule{TriggerType: TriggerOrderPaid, ConfigJSON: `{"allow_all_items":false}`}, triggerType: TriggerOrderPaid},
		{name: "item-bound", rule: db.AutomationRule{ItemID: "item-1", TriggerType: TriggerOrderPaid, ConfigJSON: `{"allow_all_items":true}`}, triggerType: TriggerOrderPaid},
		{name: "rule-trigger-mismatch", rule: db.AutomationRule{TriggerType: TriggerBuyerReviewed, ConfigJSON: `{"allow_all_items":true}`}, triggerType: TriggerOrderPaid},
		{name: "event-trigger-mismatch", rule: db.AutomationRule{TriggerType: TriggerOrderPaid, ConfigJSON: `{"allow_all_items":true}`}, triggerType: TriggerBuyerReviewed},
		{name: "invalid-json", rule: db.AutomationRule{TriggerType: TriggerOrderPaid, ConfigJSON: `not-json`}, triggerType: TriggerOrderPaid},
	}
	// testCase 是当前待判定的「不应获得授权」用例。
	for _, testCase := range rejected {
		if ruleAllowsAllItems(testCase.rule, testCase.triggerType) {
			t.Fatalf("%s 不应获得账号级规格授权", testCase.name)
		}
	}
}

// TestActionPlannerAccountWideRuleMatchesAnyOrderSpec 验证账号级授权规则的空规格动作可匹配任意订单规格。
func TestActionPlannerAccountWideRuleMatchesAnyOrderSpec(t *testing.T) {
	// actions 是未限定规格的发卡动作。
	actions := []db.AutomationAction{{ActionType: ActionSendCard, ConfigJSON: `{}`, Enabled: true}}
	// 未授权时，带规格订单不应匹配空规格动作。
	plainTask := Task{TriggerType: TriggerOrderPaid, ItemID: "item-1", SpecName: "颜色", SpecValue: "红"}
	if (actionPlanner{}).hasMatchingSendCard(plainTask, actions) {
		t.Fatal("未授权规则的账号级动作不得匹配带规格订单")
	}
	// 授权后，同一条动作应匹配任意订单规格。
	authorizedTask := plainTask
	authorizedTask.AllowAllItems = true
	if !(actionPlanner{}).hasMatchingSendCard(authorizedTask, actions) {
		t.Fatal("已授权规则的账号级动作应匹配任意订单规格")
	}
	// 无规格订单在两种情况下都应匹配。
	emptySpecTask := Task{TriggerType: TriggerOrderPaid, ItemID: "item-1"}
	if !(actionPlanner{}).hasMatchingSendCard(emptySpecTask, actions) {
		t.Fatal("无规格订单应匹配账号级动作")
	}
}

// TestCenterAccountWideOrderPaidRuleSendsForSpecifiedOrderSpec 验证显式授权的账号级付款规则可对带规格订单发卡。
func TestCenterAccountWideOrderPaidRuleSendsForSpecifiedOrderSpec(t *testing.T) {
	// ctx、store、center、sender、cleanup 保存账号级授权场景的夹具。
	ctx, store, center, sender, cleanup := newAccountWideDeliveryFixture(t, true)
	defer cleanup()
	// order 保存带规格的待发货订单事实，用于验证规格不再阻断账号级授权规则。
	order := &db.Order{OrderID: "account-wide-order", CookieID: "cid", ItemID: "item-any", BuyerID: "buyer", ChatID: "chat", SpecName: "颜色", SpecValue: "红", Quantity: "1", OrderStatus: "pending_ship"}
	// upsertErr 是写入带规格订单夹具失败的原因。
	if upsertErr := store.Orders.Upsert(ctx, order.OrderID, db.OrderUpsertOpts{CookieID: order.CookieID, ItemID: order.ItemID, BuyerID: order.BuyerID, ChatID: order.ChatID, SpecName: order.SpecName, SpecValue: order.SpecValue, Quantity: order.Quantity, OrderStatus: order.OrderStatus}); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// 显式授权的账号级规则必须对带规格订单发卡。
	if handleErr := center.HandleTask(ctx, Task{
		Source: "ws", AccountID: "cid", OrderRole: OrderRoleSeller, TriggerType: TriggerOrderPaid,
		OrderID: order.OrderID, ItemID: order.ItemID, BuyerID: order.BuyerID, ChatID: order.ChatID,
		SpecName: order.SpecName, SpecValue: order.SpecValue, Quantity: order.Quantity,
		Raw: map[string]any{"message_id": "account-wide-1"},
	}); handleErr != nil {
		t.Fatalf("账号级授权规则执行失败: %v", handleErr)
	}
	if len(sender.texts) != 1 || sender.texts[0] != "ACCOUNT-WIDE-CARD" {
		t.Fatalf("账号级授权规则未发出卡密: texts=%v", sender.texts)
	}
}

// TestCenterAccountWideOrderPaidRuleWithoutAuthorizationSendsNothing 验证未显式授权的账号级规则不发送卡密。
func TestCenterAccountWideOrderPaidRuleWithoutAuthorizationSendsNothing(t *testing.T) {
	// ctx、store、center、sender、cleanup 保存未授权场景的夹具。
	ctx, store, center, sender, cleanup := newAccountWideDeliveryFixture(t, false)
	defer cleanup()
	// order 保存带规格的待发货订单事实。
	order := &db.Order{OrderID: "unauthorized-order", CookieID: "cid", ItemID: "item-any", BuyerID: "buyer", ChatID: "chat", SpecName: "颜色", SpecValue: "红", Quantity: "1", OrderStatus: "pending_ship"}
	// upsertErr 是写入带规格订单夹具失败的原因。
	if upsertErr := store.Orders.Upsert(ctx, order.OrderID, db.OrderUpsertOpts{CookieID: order.CookieID, ItemID: order.ItemID, BuyerID: order.BuyerID, ChatID: order.ChatID, SpecName: order.SpecName, SpecValue: order.SpecValue, Quantity: order.Quantity, OrderStatus: order.OrderStatus}); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// 未授权时账号级规则不得把卡密发给规格不确定的订单。
	if handleErr := center.HandleTask(ctx, Task{
		Source: "ws", AccountID: "cid", OrderRole: OrderRoleSeller, TriggerType: TriggerOrderPaid,
		OrderID: order.OrderID, ItemID: order.ItemID, BuyerID: order.BuyerID, ChatID: order.ChatID,
		SpecName: order.SpecName, SpecValue: order.SpecValue, Quantity: order.Quantity,
		Raw: map[string]any{"message_id": "unauthorized-1"},
	}); handleErr != nil {
		t.Fatalf("未授权规则不应返回执行错误: %v", handleErr)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("未授权账号级规则不得发送卡密: texts=%v", sender.texts)
	}
}
