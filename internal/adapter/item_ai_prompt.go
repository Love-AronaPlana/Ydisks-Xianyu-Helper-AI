package adapter

import (
	"context"
	"errors"

	itemapp "xianyu-go/internal/application/items"
	"xianyu-go/internal/db"
)

// ItemAIPromptRepository 将商品级 AI 提示词应用端口适配到数据库仓储。
type ItemAIPromptRepository struct {
	// store 提供账号、商品和提示词配置的数据库访问能力。
	store *db.Store
}

// NewItemAIPromptRepository 创建商品级 AI 提示词仓储适配器。
func NewItemAIPromptRepository(store *db.Store) *ItemAIPromptRepository {
	return &ItemAIPromptRepository{store: store}
}

// AccountOwned 判断账号是否属于用户，不读取账号凭证。
func (repository *ItemAIPromptRepository) AccountOwned(ctx context.Context, userID int64, cookieID string) (bool, error) {
	if repository == nil || repository.store == nil || repository.store.Cookies == nil {
		return false, errors.New("商品 AI 提示词账号存储未初始化")
	}
	return repository.store.Cookies.ExistsOwned(ctx, userID, cookieID)
}

// ItemExists 判断账号下未软删除商品是否存在。
func (repository *ItemAIPromptRepository) ItemExists(ctx context.Context, cookieID, itemID string) (bool, error) {
	if repository == nil || repository.store == nil || repository.store.Items == nil {
		return false, errors.New("商品 AI 提示词商品存储未初始化")
	}
	// err 是商品查询错误：ErrNotFound 已被上方分支吃掉，非空即表示数据库不可用或查询失败。
	_, err := repository.store.Items.GetByCookieItem(ctx, cookieID, itemID)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// GetAIPrompt 读取数据库提示词配置并返回未配置标记。
func (repository *ItemAIPromptRepository) GetAIPrompt(ctx context.Context, cookieID, itemID string) (itemapp.ItemAIPrompt, bool, error) {
	if repository == nil || repository.store == nil || repository.store.ItemAIPrompts == nil {
		return itemapp.ItemAIPrompt{}, false, errors.New("商品 AI 提示词存储未初始化")
	}
	// row 是数据库行模型（自定义变量仍为 JSON 文本，由应用层解码），configured 表示是否已配置，err 是读取失败原因。
	row, configured, err := repository.store.ItemAIPrompts.Get(ctx, cookieID, itemID)
	if err != nil || !configured {
		return itemapp.ItemAIPrompt{}, configured, err
	}
	return itemapp.ItemAIPrompt{CookieID: row.CookieID, ItemID: row.ItemID, Strategy: row.Strategy, Prompt: row.Prompt, CustomVariablesJSON: row.CustomVariablesJSON}, true, nil
}

// SaveAIPrompt 保存应用层提示词模型到数据库行模型。
func (repository *ItemAIPromptRepository) SaveAIPrompt(ctx context.Context, prompt itemapp.ItemAIPrompt) error {
	if repository == nil || repository.store == nil || repository.store.ItemAIPrompts == nil {
		return errors.New("商品 AI 提示词存储未初始化")
	}
	return repository.store.ItemAIPrompts.Upsert(ctx, db.ItemAIPrompt{CookieID: prompt.CookieID, ItemID: prompt.ItemID, Strategy: prompt.Strategy, Prompt: prompt.Prompt, CustomVariablesJSON: prompt.CustomVariablesJSON})
}

var _ itemapp.ItemAIPromptRepository = (*ItemAIPromptRepository)(nil)
