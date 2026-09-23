package chat

import (
	"context"
	"errors"
	"testing"
	"time"
)

// handoffTestRepository 是人工接管用例的确定性替身，按账号与买家隔离保存截止时间。
type handoffTestRepository struct {
	// handoffBasicRepository 提供满足基础聊天仓储能力的方法，使替身可直接构造服务。
	handoffBasicRepository
	// owned 表示账号归属判定结果。
	owned bool
	// buyerID 是会话解析出的买家标识；为空模拟会话不存在。
	buyerID string
	// pausedUntilByKey 按“账号/买家”保存接管截止时间。
	pausedUntilByKey map[string]int64
	// ownershipErr、buyerErr、setErr、clearErr、getErr 是各步骤的可注入错误。
	ownershipErr error
	buyerErr     error
	setErr       error
	clearErr     error
	getErr       error
}

// ExistsOwned 返回预设的账号归属结论。
func (r *handoffTestRepository) ExistsOwned(context.Context, int64, string) (bool, error) {
	return r.owned, r.ownershipErr
}

// FindSessionBuyer 返回预设的会话对端买家标识。
func (r *handoffTestRepository) FindSessionBuyer(context.Context, int64, string, string) (string, error) {
	return r.buyerID, r.buyerErr
}

// SetHumanHandoff 覆盖保存接管截止时间。
func (r *handoffTestRepository) SetHumanHandoff(_ context.Context, accountID, buyerID string, pausedUntil int64) error {
	if r.setErr != nil {
		return r.setErr
	}
	if r.pausedUntilByKey == nil {
		r.pausedUntilByKey = make(map[string]int64)
	}
	r.pausedUntilByKey[accountID+"/"+buyerID] = pausedUntil
	return nil
}

// ClearHumanHandoff 删除接管记录并返回是否命中。
func (r *handoffTestRepository) ClearHumanHandoff(_ context.Context, accountID, buyerID string) (bool, error) {
	if r.clearErr != nil {
		return false, r.clearErr
	}
	// key 是本次清除的隔离键。
	key := accountID + "/" + buyerID
	// ok 是替身存储中该隔离键的存在状态；未命中说明该买家没有接管记录，按未删除处理。
	if _, ok := r.pausedUntilByKey[key]; !ok {
		return false, nil
	}
	delete(r.pausedUntilByKey, key)
	return true, nil
}

// GetHumanHandoff 读取接管截止时间并返回存在状态。
func (r *handoffTestRepository) GetHumanHandoff(_ context.Context, accountID, buyerID string) (int64, bool, error) {
	if r.getErr != nil {
		return 0, false, r.getErr
	}
	// pausedUntil、ok 是该隔离键对应的截止时间与存在状态。
	pausedUntil, ok := r.pausedUntilByKey[accountID+"/"+buyerID]
	return pausedUntil, ok, nil
}

// newHandoffTestService 构造使用固定时钟的人工接管用例服务。
func newHandoffTestService(t *testing.T, repository *handoffTestRepository) (*Service, func()) {
	t.Helper()
	// fixed 是本次测试使用的固定当前时间。
	fixed := time.Unix(1_700_000_000, 0).UTC()
	// original 保存替换前的时钟函数，测试结束后必须恢复。
	original := humanHandoffNow
	humanHandoffNow = func() time.Time { return fixed }
	return New(repository), func() { humanHandoffNow = original }
}

// TestSetHumanHandoffAppliesSelectedMinutes 验证按分钟设置接管并返回剩余时长。
func TestSetHumanHandoffAppliesSelectedMinutes(t *testing.T) {
	// repository 是记录接管写入的确定性替身。
	repository := &handoffTestRepository{owned: true, buyerID: "buyer-1"}
	// service 是被测的聊天用例服务；restore 复原被替换的全局时钟，须在用例结束前调用。
	service, restore := newHandoffTestService(t, repository)
	defer restore()
	// handoff、err 是设置后的接管状态与错误。
	handoff, err := service.SetHumanHandoff(context.Background(), 7, "acc-1", "chat-1", 5)
	if err != nil {
		t.Fatalf("设置人工接管失败: %v", err)
	}
	if !handoff.Active || handoff.RemainingSeconds != 300 || handoff.BuyerID != "buyer-1" {
		t.Fatalf("接管状态=%+v", handoff)
	}
	// 读取必须返回同一接管窗口，证明写入与读取按同一买家隔离键对齐。
	read, readErr := service.GetHumanHandoff(context.Background(), 7, "acc-1", "chat-1")
	if readErr != nil || !read.Active || read.RemainingSeconds != 300 {
		t.Fatalf("读取接管状态=%+v err=%v", read, readErr)
	}
}

// TestClearHumanHandoffEndsTakeoverImmediately 验证提前结束接管后状态立即变为未接管。
func TestClearHumanHandoffEndsTakeoverImmediately(t *testing.T) {
	// repository 是记录接管写入与清除的确定性替身，买家标识固定为 buyer-1。
	repository := &handoffTestRepository{owned: true, buyerID: "buyer-1"}
	// service 是被测的聊天用例服务；restore 复原被替换的全局时钟。
	service, restore := newHandoffTestService(t, repository)
	defer restore()
	// ctx 是设置与清除共用的调用上下文。
	ctx := context.Background()
	// err 是设置 15 分钟接管的失败原因；夹具写入失败会让后续清除断言失去意义。
	if _, err := service.SetHumanHandoff(ctx, 7, "acc-1", "chat-1", 15); err != nil {
		t.Fatalf("设置人工接管失败: %v", err)
	}
	// handoff、err 是结束后的接管状态与错误。
	handoff, err := service.ClearHumanHandoff(ctx, 7, "acc-1", "chat-1")
	if err != nil {
		t.Fatalf("结束人工接管失败: %v", err)
	}
	if handoff.Active || handoff.RemainingSeconds != 0 {
		t.Fatalf("结束后仍视为接管中: %+v", handoff)
	}
	// read、readErr 是清除后的读回状态与读取失败原因；清除必须让接管立即失效且不残留记录。
	if read, readErr := service.GetHumanHandoff(ctx, 7, "acc-1", "chat-1"); readErr != nil || read.Active {
		t.Fatalf("结束后读取仍为接管中: %+v err=%v", read, readErr)
	}
}

// TestHumanHandoffRejectsInvalidMinutes 验证超出允许范围的分钟数被拒绝且不写入任何记录。
func TestHumanHandoffRejectsInvalidMinutes(t *testing.T) {
	// repository 是记录接管写入的确定性替身，用于确认非法时长完全不落库。
	repository := &handoffTestRepository{owned: true, buyerID: "buyer-1"}
	// service 是被测的聊天用例服务；restore 复原被替换的全局时钟。
	service, restore := newHandoffTestService(t, repository)
	defer restore()
	// minutes 是当前待检查的非法时长。
	for _, minutes := range []int{0, -1, humanHandoffMaxMinutes + 1} {
		// err 是越界分钟数应触发的参数校验失败，必须精确匹配 ErrHumanHandoffInvalidMinutes。
		if _, err := service.SetHumanHandoff(context.Background(), 7, "acc-1", "chat-1", minutes); !errors.Is(err, ErrHumanHandoffInvalidMinutes) {
			t.Fatalf("时长 %d 应被拒绝，实际 err=%v", minutes, err)
		}
	}
	if len(repository.pausedUntilByKey) != 0 {
		t.Fatalf("非法时长不得写入接管记录: %+v", repository.pausedUntilByKey)
	}
}

// TestHumanHandoffRejectsNonOwnedAccountAndMissingSession 验证越权与不存在的会话被明确区分。
func TestHumanHandoffRejectsNonOwnedAccountAndMissingSession(t *testing.T) {
	// 越权账号不得进入会话解析阶段。
	notOwned := &handoffTestRepository{owned: false, buyerID: "buyer-1"}
	// service 是被测的聊天用例服务；restore 复原被替换的全局时钟。
	service, restore := newHandoffTestService(t, notOwned)
	// err 是越权账号应触发的拒绝结果，必须在解析会话之前返回 ErrHumanHandoffForbidden。
	if _, err := service.SetHumanHandoff(context.Background(), 7, "acc-1", "chat-1", 5); !errors.Is(err, ErrHumanHandoffForbidden) {
		t.Fatalf("越权账号 err=%v", err)
	}
	restore()
	// 账号归属通过但会话无法解析买家时返回会话不存在。
	missingSession := &handoffTestRepository{owned: true}
	// missingService 是复用同一时钟替换构造的第二个用例服务；missingRestore 复原全局时钟。
	missingService, missingRestore := newHandoffTestService(t, missingSession)
	// err 是会话查不到买家时应返回的查找失败，必须精确匹配 ErrHumanHandoffSessionNotFound。
	if _, err := missingService.SetHumanHandoff(context.Background(), 7, "acc-1", "chat-1", 5); !errors.Is(err, ErrHumanHandoffSessionNotFound) {
		t.Fatalf("缺少会话 err=%v", err)
	}
	// 空账号或空会话属于非法输入。
	if _, err := missingService.GetHumanHandoff(context.Background(), 7, "", "chat-1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空账号 err=%v", err)
	}
	missingRestore()
}

// TestHumanHandoffUnavailableWithoutRepository 验证仓储缺少接管能力时返回服务未启用。
func TestHumanHandoffUnavailableWithoutRepository(t *testing.T) {
	// service 使用只实现基础仓储的替身，不提供人工接管能力。
	service := New(handoffBasicRepository{})
	// err 是缺少接管端口时应返回的服务未启用结果，不得退化为普通的读取失败。
	if _, err := service.GetHumanHandoff(context.Background(), 7, "acc-1", "chat-1"); !errors.Is(err, ErrHumanHandoffUnavailable) {
		t.Fatalf("缺少接管端口 err=%v", err)
	}
}

// TestHumanHandoffPropagatesRepositoryErrors 验证各步骤的底层错误原样向上传递。
func TestHumanHandoffPropagatesRepositoryErrors(t *testing.T) {
	// wantErr 是所有失败场景共用的底层错误。
	wantErr := errors.New("存储不可用")
	// case 是当前待检查的失败注入场景。
	cases := []struct {
		// name 是该场景的测试名称。
		name string
		// repository 是被注入单一故障的替身。
		repository *handoffTestRepository
		// run 执行该场景下的人工接管调用。
		run func(*Service) error
	}{
		{name: "ownership", repository: &handoffTestRepository{ownershipErr: wantErr}, run: func(s *Service) error {
			// err 是账号归属查询注入的底层错误；用例必须原样向上传递以保留错误链。
			_, err := s.SetHumanHandoff(context.Background(), 7, "acc-1", "chat-1", 5)
			return err
		}},
		{name: "session", repository: &handoffTestRepository{owned: true, buyerErr: wantErr}, run: func(s *Service) error {
			// err 是会话买家解析注入的底层错误，不得被替换成通用的会话不存在结果。
			_, err := s.SetHumanHandoff(context.Background(), 7, "acc-1", "chat-1", 5)
			return err
		}},
		{name: "set", repository: &handoffTestRepository{owned: true, buyerID: "buyer-1", setErr: wantErr}, run: func(s *Service) error {
			// err 是接管写入注入的底层错误，必须原样返回给调用方。
			_, err := s.SetHumanHandoff(context.Background(), 7, "acc-1", "chat-1", 5)
			return err
		}},
		{name: "clear", repository: &handoffTestRepository{owned: true, buyerID: "buyer-1", clearErr: wantErr}, run: func(s *Service) error {
			// err 是接管清除注入的底层错误，必须原样返回给调用方。
			_, err := s.ClearHumanHandoff(context.Background(), 7, "acc-1", "chat-1")
			return err
		}},
		{name: "get", repository: &handoffTestRepository{owned: true, buyerID: "buyer-1", getErr: wantErr}, run: func(s *Service) error {
			// err 是接管读取注入的底层错误，必须原样返回给调用方。
			_, err := s.GetHumanHandoff(context.Background(), 7, "acc-1", "chat-1")
			return err
		}},
	}
	// testCase 是当前待验证的失败注入场景，提供故障替身与该场景的执行函数。
	for _, testCase := range cases {
		// service 是被测的聊天用例服务；restore 复原被替换的全局时钟。
		service, restore := newHandoffTestService(t, testCase.repository)
		// err 是该场景返回的错误；必须保留底层错误链。
		err := testCase.run(service)
		restore()
		if !errors.Is(err, wantErr) {
			t.Fatalf("%s err=%v, want %v", testCase.name, err, wantErr)
		}
	}
}

// TestHumanHandoffTreatsExpiredWindowAsInactive 验证已过期的接管记录按未接管返回。
func TestHumanHandoffTreatsExpiredWindowAsInactive(t *testing.T) {
	// repository 预置一个早于当前时间的接管截止时间。
	repository := &handoffTestRepository{owned: true, buyerID: "buyer-1", pausedUntilByKey: map[string]int64{"acc-1/buyer-1": 1_600_000_000}}
	// service 是被测的聊天用例服务；restore 复原被替换的全局时钟。
	service, restore := newHandoffTestService(t, repository)
	defer restore()
	// handoff、err 是读取结果与错误；过期窗口必须按未接管返回。
	handoff, err := service.GetHumanHandoff(context.Background(), 7, "acc-1", "chat-1")
	if err != nil || handoff.Active || handoff.RemainingSeconds != 0 {
		t.Fatalf("过期接管=%+v err=%v", handoff, err)
	}
}

// handoffBasicRepository 是只实现基础聊天仓储、不提供人工接管能力的替身。
type handoffBasicRepository struct{}

// ListMessages 返回空消息页，仅用于满足基础仓储能力。
func (handoffBasicRepository) ListMessages(context.Context, int64, string, string, int64, int) ([]Message, error) {
	return nil, nil
}

// ListSessions 返回空会话列表，仅用于满足基础仓储能力。
func (handoffBasicRepository) ListSessions(context.Context, int64, string, int) ([]Session, error) {
	return nil, nil
}

// ListSessionPage 返回空会话分页，仅用于满足基础仓储能力。
func (handoffBasicRepository) ListSessionPage(context.Context, int64, string, *SessionCursor, int) (SessionPage, error) {
	return SessionPage{}, nil
}
