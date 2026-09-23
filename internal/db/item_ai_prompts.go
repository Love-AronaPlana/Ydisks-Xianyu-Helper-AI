package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ItemAIPrompt 保存单个账号商品的 AI 提示词覆盖配置；该配置独立于商品软删除状态保留。
type ItemAIPrompt struct {
	// CookieID 是配置所属账号的非敏感标识。
	CookieID string
	// ItemID 是平台商品标识。
	ItemID string
	// Strategy 是提示词合并策略：inherit、append 或 override。
	Strategy string
	// Prompt 是商品级自定义提示词正文。
	Prompt string
	// CustomVariablesJSON 是自定义变量的 JSON 对象文本。
	CustomVariablesJSON string
}

// ItemAIPrompts 是商品级 AI 提示词配置仓储。
type ItemAIPrompts struct {
	// DB 保存本仓储使用的数据库连接池。
	DB *sql.DB
	// Dialect 指示当前数据库方言，供后续扩展方言 SQL 使用。
	Dialect Dialect
}

// Get 读取账号商品的 AI 提示词配置；未配置时返回 false 和零值。
func (r *ItemAIPrompts) Get(ctx context.Context, cookieID, itemID string) (ItemAIPrompt, bool, error) {
	if r == nil || r.DB == nil {
		return ItemAIPrompt{}, false, errors.New("商品 AI 提示词存储未初始化")
	}
	// prompt 承载查询返回的商品级配置；未命中时保持零值并由 configured=false 表达。
	var prompt ItemAIPrompt
	// err 是查询与扫描错误；ErrNoRows 由下方分支转换为“未配置”，其他错误原样上抛。
	err := r.DB.QueryRowContext(ctx, `SELECT cookie_id, item_id, strategy, prompt, custom_variables FROM item_ai_prompts WHERE cookie_id=? AND item_id=?`, cookieID, itemID).
		Scan(&prompt.CookieID, &prompt.ItemID, &prompt.Strategy, &prompt.Prompt, &prompt.CustomVariablesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ItemAIPrompt{}, false, nil
	}
	if err != nil {
		return ItemAIPrompt{}, false, err
	}
	return prompt, true, nil
}

// Upsert 插入或更新账号商品的 AI 提示词配置，不触碰商品软删除字段。
func (r *ItemAIPrompts) Upsert(ctx context.Context, prompt ItemAIPrompt) error {
	if r == nil || r.DB == nil {
		return errors.New("商品 AI 提示词存储未初始化")
	}
	prompt.CookieID = strings.TrimSpace(prompt.CookieID)
	prompt.ItemID = strings.TrimSpace(prompt.ItemID)
	// err 是插入或更新失败的错误；提示词配置的写入结果完全由该错误表达，因此直接返回。
	_, err := r.DB.ExecContext(ctx, `INSERT INTO item_ai_prompts (cookie_id, item_id, strategy, prompt, custom_variables, updated_at) VALUES (?,?,?,?,?,CURRENT_TIMESTAMP)`+
		dialectUpsert(r.Dialect, []string{"cookie_id", "item_id"}, map[string]string{
			"strategy":         "EXCLUDED.strategy",
			"prompt":           "EXCLUDED.prompt",
			"custom_variables": "EXCLUDED.custom_variables",
			"updated_at":       "CURRENT_TIMESTAMP",
		}), prompt.CookieID, prompt.ItemID, prompt.Strategy, prompt.Prompt, prompt.CustomVariablesJSON)
	return err
}
