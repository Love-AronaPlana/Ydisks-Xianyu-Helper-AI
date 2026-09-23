package db

import (
	"context"
	"path/filepath"
	"testing"
)

// newHandoffStore 打开隔离的 SQLite 数据库并写入一个测试账号。
func newHandoffStore(t *testing.T) (*Store, context.Context, func()) {
	t.Helper()
	// d、err 是隔离测试数据库连接与打开错误。
	d, _, err := Open(context.Background(), filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// store 是本次测试使用的数据库聚合入口。
	store := NewStore(d, DialectSQLite)
	// ctx 是夹具写入与断言共用的上下文。
	ctx := context.Background()
	// userErr 是创建测试管理员账号的写入失败原因；失败即视为夹具本身损坏。
	if _, userErr := store.Users.Create(ctx, "admin", "handoff@example.invalid", "pw"); userErr != nil {
		t.Fatalf("创建测试用户失败: %v", userErr)
	}
	// admin 是用于绑定账号所有者的测试用户。
	admin, userErr := store.Users.GetByUsername(ctx, "admin")
	if userErr != nil {
		t.Fatalf("读取测试用户失败: %v", userErr)
	}
	// saveErr 是写入 cid 测试账号及其归属关系的失败原因，后续接管隔离断言都依赖该账号。
	if saveErr := store.Cookies.Save(ctx, "cid", "unb=1;", admin.ID); saveErr != nil {
		t.Fatalf("写入测试账号失败: %v", saveErr)
	}
	return store, ctx, func() { _ = d.Close() }
}

// TestSetHumanHandoffOverwritesExistingWindow 验证手动设置会覆盖（而非只延长）既有接管窗口。
func TestSetHumanHandoffOverwritesExistingWindow(t *testing.T) {
	// store、ctx、cleanup 是隔离数据库、共享上下文与释放函数。
	store, ctx, cleanup := newHandoffStore(t)
	defer cleanup()
	// longUntil 是先写入的长接管窗口，用于确认后续短时长设置会真正缩短它。
	longUntil := int64(10_000)
	// err 是首次写入长接管窗口的失败原因；夹具窗口写不进去则覆盖断言无意义。
	if err := store.AIReply.SetHumanHandoff(ctx, "cid", "buyer-1", longUntil); err != nil {
		t.Fatalf("写入长接管窗口失败: %v", err)
	}
	// shortUntil 是操作者随后选择的较短截止时间，必须覆盖原值。
	shortUntil := int64(5_000)
	// err 是覆盖写入短接管窗口的失败原因；设置语义必须是覆盖而不是取最大值。
	if err := store.AIReply.SetHumanHandoff(ctx, "cid", "buyer-1", shortUntil); err != nil {
		t.Fatalf("覆盖接管窗口失败: %v", err)
	}
	// pausedUntil、found、readErr 是读取到的截止时间、存在状态与错误。
	pausedUntil, found, readErr := store.AIReply.GetHumanHandoff(ctx, "cid", "buyer-1")
	if readErr != nil || !found || pausedUntil != shortUntil {
		t.Fatalf("覆盖后接管窗口=(%d,%v,%v)", pausedUntil, found, readErr)
	}
}

// TestGetHumanHandoffReportsMissingRecord 验证没有接管记录时返回未找到而不是错误。
func TestGetHumanHandoffReportsMissingRecord(t *testing.T) {
	// store、ctx、cleanup 是隔离数据库、共享上下文与释放函数。
	store, ctx, cleanup := newHandoffStore(t)
	defer cleanup()
	// pausedUntil、found、err 是未记录买家的读取结果；必须返回未找到而不是错误。
	if pausedUntil, found, err := store.AIReply.GetHumanHandoff(ctx, "cid", "buyer-missing"); err != nil || found || pausedUntil != 0 {
		t.Fatalf("未记录接管=(%d,%v,%v)", pausedUntil, found, err)
	}
}

// TestClearHumanHandoffEndsActiveWindow 验证清除后接管立即结束，并且不影响其它买家与账号。
func TestClearHumanHandoffEndsActiveWindow(t *testing.T) {
	// store、ctx、cleanup 是隔离数据库、共享上下文与释放函数。
	store, ctx, cleanup := newHandoffStore(t)
	defer cleanup()
	// err 是为 buyer-1 写入接管窗口的失败原因；该窗口随后必须被清除。
	if err := store.AIReply.SetHumanHandoff(ctx, "cid", "buyer-1", 9_000); err != nil {
		t.Fatalf("写入接管失败: %v", err)
	}
	// err 是为同一账号下 buyer-2 写入接管窗口的失败原因，用于验证清除不波及其它买家。
	if err := store.AIReply.SetHumanHandoff(ctx, "cid", "buyer-2", 9_000); err != nil {
		t.Fatalf("写入第二个买家接管失败: %v", err)
	}
	// cleared、clearErr 是清除结果与错误；命中记录时应为 true。
	cleared, clearErr := store.AIReply.ClearHumanHandoff(ctx, "cid", "buyer-1")
	if clearErr != nil || !cleared {
		t.Fatalf("清除接管=(%v,%v)", cleared, clearErr)
	}
	// 清除必须让接管窗口在原本有效的时刻立即失效。
	active, activeErr := store.AIReply.IsHumanHandoffActive(ctx, "cid", "buyer-1", 8_000)
	if activeErr != nil || active {
		t.Fatalf("清除后接管仍生效: active=%v err=%v", active, activeErr)
	}
	// 同一账号下其它买家的接管窗口不得被连带清除。
	otherActive, otherErr := store.AIReply.IsHumanHandoffActive(ctx, "cid", "buyer-2", 8_000)
	if otherErr != nil || !otherActive {
		t.Fatalf("清除影响其它买家: active=%v err=%v", otherActive, otherErr)
	}
	// 清除后必须可以重新接管。
	if err := store.AIReply.SetHumanHandoff(ctx, "cid", "buyer-1", 20_000); err != nil {
		t.Fatalf("重新接管失败: %v", err)
	}
	// reactivated、err 是清除后重新接管的状态与查询失败原因；清除成功的记录必须允许再次写入。
	if reactivated, err := store.AIReply.IsHumanHandoffActive(ctx, "cid", "buyer-1", 19_000); err != nil || !reactivated {
		t.Fatalf("重新接管未生效: active=%v err=%v", reactivated, err)
	}
}

// TestClearHumanHandoffIsIdempotent 验证清除不存在的接管记录按幂等处理且不报错。
func TestClearHumanHandoffIsIdempotent(t *testing.T) {
	// store、ctx、cleanup 是隔离数据库、共享上下文与释放函数。
	store, ctx, cleanup := newHandoffStore(t)
	defer cleanup()
	// cleared、err 是清除结果与错误；没有记录时应返回 false 且无错误。
	cleared, err := store.AIReply.ClearHumanHandoff(ctx, "cid", "buyer-none")
	if err != nil || cleared {
		t.Fatalf("清除不存在的接管=(%v,%v)", cleared, err)
	}
}

// TestHumanHandoffIsIsolatedPerAccount 验证同一买家在不同账号下的接管互不影响。
func TestHumanHandoffIsIsolatedPerAccount(t *testing.T) {
	// store、ctx、cleanup 是隔离数据库、共享上下文与释放函数。
	store, ctx, cleanup := newHandoffStore(t)
	defer cleanup()
	// otherUser 是第二个账号的所有者，用于建立独立的账号隔离域。
	otherUser, otherErr := store.Users.GetByUsername(ctx, "admin")
	if otherErr != nil {
		t.Fatalf("读取测试用户失败: %v", otherErr)
	}
	// err 是写入 cid-other 第二个账号的失败原因；两个账号都归属同一用户用于验证账号级隔离。
	if err := store.Cookies.Save(ctx, "cid-other", "unb=2;", otherUser.ID); err != nil {
		t.Fatalf("写入第二个账号失败: %v", err)
	}
	// err 是仅向 cid 写入接管窗口的失败原因；cid-other 不应因此产生接管记录。
	if err := store.AIReply.SetHumanHandoff(ctx, "cid", "buyer-1", 9_000); err != nil {
		t.Fatalf("写入第一个账号接管失败: %v", err)
	}
	// 相同买家标识在另一个账号下不得被判定为接管中。
	otherActive, err := store.AIReply.IsHumanHandoffActive(ctx, "cid-other", "buyer-1", 8_000)
	if err != nil || otherActive {
		t.Fatalf("账号隔离失效: active=%v err=%v", otherActive, err)
	}
}
