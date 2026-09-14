package adapter

import (
	"context"
	"errors"
	"testing"

	itemapp "xianyu-go/internal/application/items"
	"xianyu-go/internal/xianyu/mtop"
)

// TestItemSyncRepositoryUsesListMultiSpecMarkers 验证同步连续两次采用列表响应中的多规格标记，且不会发起商品详情请求。
func TestItemSyncRepositoryUsesListMultiSpecMarkers(t *testing.T) {
	// store、cleanup 保存测试隔离的 SQLite 数据库及其释放责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// detailCalls 记录不应由同步流程发起的详情探测次数。
	detailCalls := 0
	// client 提供两次可变的列表结果；detect 仅用于捕获错误的详情调用。
	client := &itemSyncListClient{
		allResult: &mtop.ItemListResult{Items: []mtop.ItemListItem{{ID: "list-spec-item", Title: "列表多规格", IsMultiSpec: true}}},
		detect: func(context.Context, string, string) (bool, error) {
			detailCalls++
			return false, errors.New("商品同步不应请求详情")
		},
	}
	// repository 使用同一平台替身执行两次全量同步。
	repository := NewItemSyncRepository(store, func() mtop.Client { return client }, nil, nil, nil)
	// query 描述管理员对测试账号发起的同步请求。
	query := itemapp.SyncQuery{UserID: 1, CookieID: "cid", PageSize: 20, MaxPages: 1}
	// firstResult、firstErr 保存列表首次报告多规格的同步结果。
	firstResult, firstErr := repository.SyncAll(context.Background(), query)
	if firstErr != nil || firstResult.SavedCount != 1 || detailCalls != 0 {
		t.Fatalf("首次列表同步异常 result=%+v detailCalls=%d err=%v", firstResult, detailCalls, firstErr)
	}
	// firstItem、firstItemErr 验证列表多规格标记直接写入本地商品。
	firstItem, firstItemErr := store.Items.Get(context.Background(), "cid", "list-spec-item")
	if firstItemErr != nil || !firstItem.IsMultiSpec {
		t.Fatalf("列表多规格标记未写入 item=%+v err=%v", firstItem, firstItemErr)
	}
	// nextResult 表示平台列表在下一次同步中已改为单规格。
	nextResult := &mtop.ItemListResult{Items: []mtop.ItemListItem{{ID: "list-spec-item", Title: "列表单规格", IsMultiSpec: false}}}
	client.allResult = nextResult
	// secondResult、secondErr 保存列表第二次报告单规格的同步结果。
	secondResult, secondErr := repository.SyncAll(context.Background(), query)
	if secondErr != nil || secondResult.SavedCount != 1 || detailCalls != 0 {
		t.Fatalf("第二次列表同步异常 result=%+v detailCalls=%d err=%v", secondResult, detailCalls, secondErr)
	}
	// secondItem、secondItemErr 验证列表单规格标记能够覆盖历史多规格值。
	secondItem, secondItemErr := store.Items.Get(context.Background(), "cid", "list-spec-item")
	if secondItemErr != nil || secondItem.IsMultiSpec || secondItem.ItemTitle != "列表单规格" {
		t.Fatalf("列表单规格标记未覆盖历史值 item=%+v err=%v", secondItem, secondItemErr)
	}
}
