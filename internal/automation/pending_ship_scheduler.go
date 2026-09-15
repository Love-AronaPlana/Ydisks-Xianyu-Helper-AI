package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"xianyu-go/internal/db"
)

// scanPendingShipDeliveries 兜底扫描没有付款运行的待发货订单并补触发自动发货。
func (s *Scheduler) scanPendingShipDeliveries(ctx context.Context) {
	if ctx == nil {
		return
	}
	// scanCtx、cancel 为直接调用入口提供独立预算及释放函数。
	scanCtx, cancel := context.WithTimeout(ctx, s.pendingShipScanBudgetValue())
	defer cancel()
	_ = s.scanPendingShipDeliveriesWithContextAndLimit(scanCtx, s.pendingShipScanMaxTasksValue())
}

// scanPendingShipDeliveriesWithContextAndLimit 在预算与任务额度内补触发待发货订单。
func (s *Scheduler) scanPendingShipDeliveriesWithContextAndLimit(ctx context.Context, remainingTasks int) (leftTasks int) {
	leftTasks = remainingTasks
	if strings.TrimSpace(os.Getenv(pendingShipCatchupEnv)) == "0" || s == nil || s.center == nil || s.center.store == nil || s.center.store.Automation == nil || leftTasks <= 0 {
		return
	}
	// triggeredCount 统计本轮实际领取的订单数。
	triggeredCount := 0
	// afterOrderID 保存分页扫描的稳定订单游标。
	afterOrderID := ""
	for {
		if ctx.Err() != nil {
			s.center.logger.Warn("待发货兜底扫描达到本轮时间预算", "err", ctx.Err())
			return
		}
		// orders、err 保存当前候选页及查询错误。
		orders, err := s.center.store.Automation.PendingShipOrdersWithoutPaidRunAfter(ctx, afterOrderID, pendingShipScanPageSize)
		if err != nil {
			s.center.logger.Warn("扫描待发货兜底订单失败", "err", err)
			return
		}
		if len(orders) == 0 {
			return
		}
		// order 表示当前待发货候选订单。
		for _, order := range orders {
			if ctx.Err() != nil || leftTasks <= 0 {
				if ctx.Err() != nil {
					s.center.logger.Warn("待发货兜底扫描达到本轮时间预算", "err", ctx.Err())
				} else {
					s.center.logger.Info("待发货兜底扫描达到本轮任务上限", "count", triggeredCount)
				}
				return
			}
			afterOrderID = order.OrderID
			// allowed、allowErr 保存账号自动化门禁结果。
			allowed, allowErr := s.center.accountAutomationAllowed(ctx, order.CookieID)
			if allowErr != nil {
				s.center.logger.Warn("检查待发货兜底账号状态失败", "account", order.CookieID, "order_id", order.OrderID, "err", allowErr)
				continue
			}
			if !allowed {
				continue
			}
			// paid、paidErr 保存账号自动确认发货开关及读取错误。
			paid, paidErr := s.center.paidDeliveryAutoConfirmEnabled(ctx, order.CookieID)
			if paidErr != nil {
				s.center.logger.Warn("检查待发货兜底自动确认发货开关失败", "account", order.CookieID, "order_id", order.OrderID, "err", paidErr)
				continue
			}
			if !paid {
				continue
			}
			if !s.center.accountSenderReady(order.CookieID) {
				s.center.logger.Info("账号 WebSocket 尚未就绪，待发货兜底任务等待下次扫描", "account", order.CookieID, "order_id", order.OrderID)
				continue
			}
			if !s.claimPendingShipAttempt(order.OrderID) {
				continue
			}
			triggeredCount++
			leftTasks--
			s.center.logger.Info("付款系统消息缺失，按订单状态补触发自动发货", "account", order.CookieID, "order_id", order.OrderID, "item_id", order.ItemID)
			// taskCtx、cancel 为单个兜底任务提供执行预算及释放函数。
			taskCtx, cancel := context.WithTimeout(ctx, pendingShipTaskTimeout)
			// task 保存待发货订单转换后的自动化任务载荷。
			task := Task{Source: "scheduler", AccountID: order.CookieID, TriggerType: TriggerOrderPaid, ChatID: order.ChatID, OrderID: order.OrderID, ItemID: order.ItemID, BuyerID: order.BuyerID, Text: "付款系统消息缺失，按订单状态补触发自动发货", Raw: map[string]any{"source": "scheduler", "order_id": order.OrderID}}
			// err 保存兜底任务执行错误；失败只影响当前订单。
			if err := s.center.HandleTask(taskCtx, task); err != nil {
				s.center.logger.Warn("待发货兜底任务执行失败", "account", order.CookieID, "order_id", order.OrderID, "err", err)
			}
			cancel()
		}
		if len(orders) < pendingShipScanPageSize {
			return
		}
	}
}

// pendingShipResumeFrozenPlan 从运行的原始事件快照恢复冻结的动作计划，并判定能否自动续跑。
func pendingShipResumeFrozenPlan(candidate db.PendingShipResume) ([]db.AutomationAction, bool, error) {
	// original 保存运行创建时冻结的任务事实与完整动作计划。
	var original Task
	// err 保存快照解析错误；历史快照损坏时不能猜测缺失的动作。
	if err := json.Unmarshal([]byte(candidate.RawEventJSON), &original); err != nil {
		return nil, false, fmt.Errorf("待发货续跑运行的原始计划无法解析: %w", err)
	}
	if original.AccountID != candidate.Order.CookieID || original.OrderID != candidate.Order.OrderID || len(original.ActionPlan) == 0 {
		return nil, false, fmt.Errorf("待发货续跑运行的原始计划缺失或归属不符")
	}
	if candidate.ActionCursor < 0 || candidate.ActionCursor > len(original.ActionPlan) {
		return nil, false, fmt.Errorf("待发货续跑运行的游标越界: %d", candidate.ActionCursor)
	}
	if !pendingShipOnlyIdempotentTail(original.ActionPlan[candidate.ActionCursor:]) {
		return nil, false, nil
	}
	return original.ActionPlan, true, nil
}

// pendingShipOnlyIdempotentTail 判断剩余动作是否只包含平台侧幂等的状态动作。
func pendingShipOnlyIdempotentTail(remaining []db.AutomationAction) bool {
	if len(remaining) == 0 {
		return false
	}
	// action 是剩余动作中的一个，需要确认其不会再次联系买家。
	for _, action := range remaining {
		if action.ActionType != ActionConfirmShipment {
			return false
		}
	}
	return true
}

// claimPendingShipAttempt 领取一次兜底尝试；处于冷却窗口内的订单返回 false。
func (s *Scheduler) claimPendingShipAttempt(orderID string) bool {
	if s == nil {
		return false
	}
	s.pendingShipMu.Lock()
	defer s.pendingShipMu.Unlock()
	if s.pendingShipCooldown == nil {
		// pendingShipCooldown 延迟初始化，兼容测试或历史调用方直接构造 Scheduler 的场景。
		s.pendingShipCooldown = make(map[string]time.Time)
	}
	// now 是本次冷却判断的统一时间基准，避免同一轮内多次取时。
	now := time.Now()
	// last、seen 保存该订单上次兜底触发时间以及是否存在。
	if last, seen := s.pendingShipCooldown[orderID]; seen && now.Sub(last) < defaultPendingShipCooldown {
		return false
	}
	s.pendingShipCooldown[orderID] = now
	if len(s.pendingShipCooldown) > 4096 {
		// key、ts 是过期条目的订单号与记录时间。
		for key, ts := range s.pendingShipCooldown {
			if now.Sub(ts) >= defaultPendingShipCooldown {
				delete(s.pendingShipCooldown, key)
			}
		}
	}
	return true
}

// releasePendingShipAttempt 释放已知未取得数据库运行权的冷却预约。
func (s *Scheduler) releasePendingShipAttempt(orderID string) {
	if s == nil {
		return
	}
	s.pendingShipMu.Lock()
	defer s.pendingShipMu.Unlock()
	delete(s.pendingShipCooldown, orderID)
}
