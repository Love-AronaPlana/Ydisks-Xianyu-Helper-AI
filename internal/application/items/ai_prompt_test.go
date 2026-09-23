package items

import (
	"context"
	"strings"
	"testing"
)

// aiPromptRepositoryFake 是商品级提示词测试使用的内存仓储。
type aiPromptRepositoryFake struct {
	// stored 保存最后一次写入的提示词配置，用于断言内容未被截断。
	stored ItemAIPrompt
	// owned 控制账号归属查询结果。
	owned bool
	// exists 控制商品存在性查询结果。
	exists bool
}

// AccountOwned 返回预设的账号归属结果。
func (f *aiPromptRepositoryFake) AccountOwned(context.Context, int64, string) (bool, error) {
	return f.owned, nil
}

// ItemExists 返回预设的商品存在性结果。
func (f *aiPromptRepositoryFake) ItemExists(context.Context, string, string) (bool, error) {
	return f.exists, nil
}

// GetAIPrompt 返回未配置状态，避免测试依赖持久化内容。
func (f *aiPromptRepositoryFake) GetAIPrompt(context.Context, string, string) (ItemAIPrompt, bool, error) {
	return ItemAIPrompt{}, false, nil
}

// SaveAIPrompt 记录写入内容供断言使用。
func (f *aiPromptRepositoryFake) SaveAIPrompt(_ context.Context, prompt ItemAIPrompt) error {
	f.stored = prompt
	return nil
}

// TestSaveAIPromptAcceptsUnlimitedLength 验证商品级提示词不再有长度上限。
func TestSaveAIPromptAcceptsUnlimitedLength(t *testing.T) {
	// repository 是记录写入内容的内存仓储。
	repository := &aiPromptRepositoryFake{owned: true, exists: true}
	// service 是待验证的商品级提示词服务。
	service, err := NewItemAIPromptService(repository)
	if err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}
	// longPrompt 是远超旧上限（8000 字符）的提示词正文。
	longPrompt := strings.Repeat("商品提示词内容", 20000)
	if len([]rune(longPrompt)) <= 8000 {
		t.Fatalf("测试提示词长度不足: %d", len([]rune(longPrompt)))
	}
	// saved、saveErr 是保存结果与错误；超长提示词必须被接受且不截断。
	saved, saveErr := service.SaveAIPrompt(context.Background(), 7, ItemAIPrompt{
		CookieID: "cid", ItemID: "item-1", Strategy: "override", Prompt: longPrompt,
		CustomVariables: map[string]string{},
	})
	if saveErr != nil {
		t.Fatalf("超长提示词应被接受: %v", saveErr)
	}
	if len([]rune(saved.Prompt)) != len([]rune(longPrompt)) || len([]rune(repository.stored.Prompt)) != len([]rune(longPrompt)) {
		t.Fatalf("提示词被截断: 返回 %d 字符，落库 %d 字符，期望 %d",
			len([]rune(saved.Prompt)), len([]rune(repository.stored.Prompt)), len([]rune(longPrompt)))
	}
	// 其它边界仍然生效：策略非法必须继续拒绝。
	if _, invalidErr := service.SaveAIPrompt(context.Background(), 7, ItemAIPrompt{
		CookieID: "cid", ItemID: "item-1", Strategy: "unknown", Prompt: "正文",
	}); invalidErr == nil {
		t.Fatal("非法策略仍应被拒绝")
	}
}
