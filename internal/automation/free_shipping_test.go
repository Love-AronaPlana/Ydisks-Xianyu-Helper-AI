package automation

import (
	"context"
	"strings"
	"testing"

	"xianyu-go/internal/db"
)

// TestPaidDeliveryUsesNormalConsignForBargainReadyOrder 验证成功小刀后的付款自动发货先发卡，再走普通确认发货而非免拼。
func TestPaidDeliveryUsesNormalConsignForBargainReadyOrder(t *testing.T) {
	// store、cleanup 保存付款自动发货主链路使用的测试数据库及其清理函数。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 是创建订单、规则和执行自动化任务共用的上下文。
	ctx := context.Background()
	// admin、adminErr 保存规则归属管理员及读取错误。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// enabled 是同时打开付款自动发货和订单状态确认开关的设置值。
	enabled := true
	// _, settingsErr 保存账号自动化开关写入结果。
	_, settingsErr := store.Cookies.UpdateSettings(ctx, "cid", db.AccountSettingsUpdate{UserID: admin.ID, AutoConfirm: &enabled, AutoConsign: &enabled})
	if settingsErr != nil {
		t.Fatal(settingsErr)
	}
	// bargain 表示订单列表同步已确认当前订单属于砍价活动。
	bargain := true
	// orderErr 保存砍价订单事实写入错误。
	orderErr := store.Orders.Upsert(ctx, "paid-bargain-order", db.OrderUpsertOpts{CookieID: "cid", ItemID: "10001", BuyerID: "20002", ChatID: "chat-1", OrderStatus: "pending_ship", IsBargain: &bargain})
	if orderErr != nil {
		t.Fatal(orderErr)
	}
	// cardID、cardErr 保存满足付款自动发货完整性规则的一次性数据卡密组及创建错误。
	cardID, cardErr := store.Cards.Create(ctx, &db.CardFull{Name: "砍价免拼测试卡", Type: "data", DataContent: "BARGAIN-CODE", Enabled: true, UserID: admin.ID})
	if cardErr != nil {
		t.Fatal(cardErr)
	}
	// _, ruleErr 保存先发货内容后确认平台状态的付款规则创建结果。
	_, ruleErr := store.Automation.Create(ctx, db.AutomationRuleInput{UserID: admin.ID, CookieID: "cid", ItemID: "10001", Name: "砍价免拼", TriggerType: TriggerOrderPaid, Enabled: true, Actions: []db.AutomationActionInput{{ActionType: ActionSendCard, CardID: cardID, DeliveryCount: 1, ConfigJSON: `{}`, Enabled: true, SortOrder: 1}, {ActionType: ActionConfirmShipment, Enabled: true, SortOrder: 2}}})
	if ruleErr != nil {
		t.Fatal(ruleErr)
	}
	// client 是断言最终阶段使用普通确认发货端点的 MTOP 内存替身。
	client := &fakeMTop{consignOk: true, consignRet: []string{"SUCCESS::调用成功"}}
	// sender 是接收测试卡密内容的在线消息发送替身。
	sender := &testSender{}
	// center 是注入免拼替身与在线发送器的自动化中心。
	center := NewWithDependencies(store, testSenderProvider{sender: sender}, nil, CenterDependencies{MTop: client})
	// handleErr 保存付款自动发货执行结果。
	handleErr := center.HandleTask(ctx, Task{AccountID: "cid", TriggerType: TriggerOrderPaid, OrderID: "paid-bargain-order", ItemID: "10001", BuyerID: "20002", ChatID: "chat-1"})
	if handleErr != nil {
		t.Fatalf("砍价付款自动发货失败: %v", handleErr)
	}
	if len(sender.texts) != 1 || client.freeShippingCalls != 0 || client.consignCalls != 1 {
		t.Fatalf("成功小刀后发卡和普通确认发货顺序异常: messages=%+v free=%d consign=%d", sender.texts, client.freeShippingCalls, client.consignCalls)
	}
}

// TestConfirmShipmentUsesNormalConsignForBargainOrder 验证砍价订单的最终确认发货始终使用普通确认发货接口。
func TestConfirmShipmentUsesNormalConsignForBargainOrder(t *testing.T) {
	// store、cleanup 保存自动化确认发货所需的本地数据库及资源释放函数。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 是本测试所有数据库与自动化操作共用的非取消上下文。
	ctx := context.Background()
	// client 是记录普通确认与免拼确认调用的 MTOP 内存替身。
	client := &fakeMTop{consignOk: true, consignRet: []string{"SUCCESS::调用成功"}}
	// center 是使用替身 MTOP 客户端的自动化中心。
	center := NewWithDependencies(store, nil, nil, CenterDependencies{MTop: client})
	// task 是已确认砍价活动且由人工请求立即确认的平台订单。
	task := Task{AccountID: "cid", OrderID: "bargain-order", ItemID: "10001", BuyerID: "20002", IsBargain: true, ForceConfirmShipment: true}
	// confirmErr 保存普通确认发货和本地状态收口过程中的错误。
	confirmErr := center.confirmShipment(ctx, task)
	if confirmErr != nil {
		t.Fatalf("砍价订单普通确认发货失败: %v", confirmErr)
	}
	if client.freeShippingCalls != 0 || client.consignCalls != 1 || client.consignOrderIn != task.OrderID {
		t.Fatalf("砍价订单调用路径错误: free=%d consign=%d order=%q", client.freeShippingCalls, client.consignCalls, client.consignOrderIn)
	}
	// order、orderErr 保存免拼成功后读取到的本地订单发货事实。
	order, orderErr := store.Orders.Get(ctx, task.OrderID)
	if orderErr != nil || order.OrderStatus != "shipped" || !order.SystemShipped {
		t.Fatalf("普通确认成功后的订单状态错误: order=%+v err=%v", order, orderErr)
	}
}

// TestBargainPendingRejectsIncompletePlatformIdentifiers 验证免拼阶段缺少平台标识时在网络调用前安全停止。
func TestBargainPendingRejectsIncompletePlatformIdentifiers(t *testing.T) {
	// store、cleanup 保存本测试使用的自动化存储及清理函数。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// client 是若被错误调用即可暴露问题的 MTOP 内存替身。
	client := &fakeMTop{freeShippingOK: true}
	// center 是注入上述替身的自动化中心。
	center := NewWithDependencies(store, nil, nil, CenterDependencies{MTop: client})
	// freeShipErr 保存缺少买家标识时的免拼阶段预检拒绝结果。
	freeShipErr := center.actions.freeShipBargain(context.Background(), Task{AccountID: "cid", OrderID: "bargain-incomplete", ItemID: "10001", IsBargain: true})
	if freeShipErr == nil || !strings.Contains(freeShipErr.Error(), "免拼发货缺少订单ID、商品ID或买家ID") || client.freeShippingCalls != 0 || client.consignCalls != 0 {
		t.Fatalf("不完整砍价订单未在网络前拒绝: err=%v free=%d consign=%d", freeShipErr, client.freeShippingCalls, client.consignCalls)
	}
}

// TestBargainPendingRunsOnlyIndependentFreeShipping 验证“待刀成”只读取自动免拼开关并调用免拼，不发卡也不确认发货。
func TestBargainPendingRunsOnlyIndependentFreeShipping(t *testing.T) {
	// store、cleanup 保存隔离的自动化数据库及资源释放函数。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 保存测试共用的无取消上下文。
	ctx := context.Background()
	// admin、adminErr 保存账号所属管理员和读取错误。
	admin, adminErr := store.Users.GetByUsername(ctx, "admin")
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	// enabled 是本测试显式开启的独立自动免拼开关。
	enabled := true
	// settingsErr 保存独立开关写入错误。
	if _, settingsErr := store.Cookies.UpdateSettings(ctx, "cid", db.AccountSettingsUpdate{UserID: admin.ID, AutoBargain: &enabled}); settingsErr != nil {
		t.Fatal(settingsErr)
	}
	// client 是记录阶段接口选择的 MTOP 内存替身。
	client := &fakeMTop{freeShippingOK: true, freeShippingRet: []string{"SUCCESS::调用成功"}}
	// sender 是若被错误使用即可暴露抢跑的在线消息替身。
	sender := &testSender{}
	// center 是待验证的自动化中心。
	center := NewWithDependencies(store, testSenderProvider{sender: sender}, nil, CenterDependencies{MTop: client})
	// handleErr 保存待刀成 WS 任务的执行错误。
	handleErr := center.HandleTask(ctx, Task{Source: "ws", AccountID: "cid", TriggerType: TriggerBargainPending, OrderID: "bargain-pending", ItemID: "10001", BuyerID: "20002", ChatID: "chat"})
	if handleErr != nil {
		t.Fatalf("待刀成免拼失败: %v", handleErr)
	}
	if client.freeShippingCalls != 1 || client.consignCalls != 0 || len(sender.texts) != 0 {
		t.Fatalf("待刀成阶段发生发卡或确认发货抢跑: free=%d consign=%d messages=%+v", client.freeShippingCalls, client.consignCalls, sender.texts)
	}
}

// TestMergeOrderIntoTaskCarriesBargainFlag 验证 WS、调度和延迟恢复共用的订单事实补全不会丢失免拼发货标记。
func TestMergeOrderIntoTaskCarriesBargainFlag(t *testing.T) {
	// order 是订单同步已明确标记为砍价活动的本地订单事实。
	order := seedAutomationOrder("merge-bargain", true)
	// merged 是使用本地订单补全后的自动化任务快照。
	merged := mergeOrderIntoTask(Task{}, order)
	if !merged.IsBargain {
		t.Fatal("订单免拼标记未进入自动化任务")
	}
}

// seedAutomationOrder 构造仅供任务事实合并测试使用的非敏感订单模型；bargain 控制平台同步标记。
func seedAutomationOrder(orderID string, bargain bool) *db.Order {
	// bargainValue 把布尔测试入参转换为数据库订单模型使用的整数标记。
	bargainValue := 0
	if bargain {
		bargainValue = 1
	}
	return &db.Order{OrderID: orderID, IsBargain: bargainValue}
}
