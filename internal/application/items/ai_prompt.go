package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrAIPromptNotFound 表示账号或商品不存在，避免向调用方泄露跨用户资源信息。
var ErrAIPromptNotFound = errors.New("商品或账号不存在")

// ErrAIPromptForbidden 表示请求用户不拥有目标账号。
var ErrAIPromptForbidden = errors.New("无权访问该账号")

// ErrAIPromptInvalid 表示商品级提示词输入不符合业务约束。
var ErrAIPromptInvalid = errors.New("商品 AI 提示词输入无效")

// ItemAIPrompt 是商品级 AI 提示词应用模型。
type ItemAIPrompt struct {
	// CookieID 是配置所属账号标识。
	CookieID string `json:"cookie_id"`
	// ItemID 是平台商品标识。
	ItemID string `json:"item_id"`
	// Strategy 是提示词策略：inherit、append 或 override。
	Strategy string `json:"strategy"`
	// Prompt 是商品级提示词正文；长度不设上限，可用长度由模型上下文窗口决定。
	Prompt string `json:"prompt"`
	// CustomVariables 是供提示词渲染使用的自定义变量。
	CustomVariables map[string]string `json:"custom_variables"`
	// CustomVariablesJSON 是仓储适配器暂存的 JSON 文本，不进入 HTTP 响应。
	CustomVariablesJSON string `json:"-"`
}

// ItemAIPromptRepository 定义商品级 AI 提示词服务所需的最小数据库能力。
type ItemAIPromptRepository interface {
	// AccountOwned 判断账号是否属于指定用户。
	AccountOwned(context.Context, int64, string) (bool, error)
	// ItemExists 判断账号下未软删除商品是否存在。
	ItemExists(context.Context, string, string) (bool, error)
	// GetAIPrompt 读取已保存配置，未配置时返回 false。
	GetAIPrompt(context.Context, string, string) (ItemAIPrompt, bool, error)
	// SaveAIPrompt 持久化商品级提示词配置。
	SaveAIPrompt(context.Context, ItemAIPrompt) error
}

// ItemAIPromptService 编排商品级 AI 提示词的归属、校验和持久化。
type ItemAIPromptService struct {
	// repository 提供账号、商品和配置的持久化能力。
	repository ItemAIPromptRepository
}

// NewItemAIPromptService 创建商品级 AI 提示词服务。
func NewItemAIPromptService(repository ItemAIPromptRepository) (*ItemAIPromptService, error) {
	if repository == nil {
		return nil, errors.New("商品 AI 提示词仓储端口不能为空")
	}
	return &ItemAIPromptService{repository: repository}, nil
}

// GetAIPrompt 读取用户有权访问商品的提示词；未配置时返回 inherit 默认模型。
func (service *ItemAIPromptService) GetAIPrompt(ctx context.Context, userID int64, cookieID, itemID string) (ItemAIPrompt, error) {
	if service == nil || service.repository == nil {
		return ItemAIPrompt{}, errors.New("商品 AI 提示词服务未初始化")
	}
	// cookieID、itemID 是去除首尾空白后的账号与商品标识，err 承载标识非法时的校验错误。
	cookieID, itemID, err := validateAIPromptTarget(userID, cookieID, itemID)
	if err != nil {
		return ItemAIPrompt{}, err
	}
	// owned 表示该账号是否归属于当前请求用户，err 是归属查询失败时的数据库错误，用于区分“无权”与“存储不可用”。
	owned, err := service.repository.AccountOwned(ctx, userID, cookieID)
	if err != nil {
		return ItemAIPrompt{}, err
	}
	if !owned {
		return ItemAIPrompt{}, ErrAIPromptForbidden
	}
	// exists 表示账号下该商品是否仍未软删除，err 是商品存在性查询失败时的数据库错误。
	exists, err := service.repository.ItemExists(ctx, cookieID, itemID)
	if err != nil {
		return ItemAIPrompt{}, err
	}
	if !exists {
		return ItemAIPrompt{}, ErrAIPromptNotFound
	}
	// prompt 是已保存的商品级提示词配置，configured 表示该商品是否已存在配置记录，err 是配置读取失败的数据库错误。
	prompt, configured, err := service.repository.GetAIPrompt(ctx, cookieID, itemID)
	if err != nil {
		return ItemAIPrompt{}, err
	}
	if !configured {
		return ItemAIPrompt{CookieID: cookieID, ItemID: itemID, Strategy: "inherit", CustomVariables: map[string]string{}}, nil
	}
	return decodeAIPrompt(prompt)
}

// SaveAIPrompt 校验用户归属、商品存在性和变量边界后保存提示词配置。
func (service *ItemAIPromptService) SaveAIPrompt(ctx context.Context, userID int64, prompt ItemAIPrompt) (ItemAIPrompt, error) {
	if service == nil || service.repository == nil {
		return ItemAIPrompt{}, errors.New("商品 AI 提示词服务未初始化")
	}
	// cookieID、itemID 是标准化后的账号与商品标识，err 在标识非法或用户 ID 无效时非空并直接作为响应错误。
	cookieID, itemID, err := validateAIPromptTarget(userID, prompt.CookieID, prompt.ItemID)
	if err != nil {
		return ItemAIPrompt{}, err
	}
	// err 是策略、正文长度或自定义变量边界校验失败的业务错误，包装 ErrAIPromptInvalid 以便上层映射 HTTP 状态。
	if err := validateAIPromptInput(prompt); err != nil {
		return ItemAIPrompt{}, err
	}
	// owned 表示目标账号是否属于当前请求用户，err 是归属查询失败时的数据库错误，未归属时按无权处理避免泄露跨用户资源。
	owned, err := service.repository.AccountOwned(ctx, userID, cookieID)
	if err != nil {
		return ItemAIPrompt{}, err
	}
	if !owned {
		return ItemAIPrompt{}, ErrAIPromptForbidden
	}
	// exists 表示账号下商品是否仍然存在（未软删除），err 是存在性查询失败时的数据库错误。
	exists, err := service.repository.ItemExists(ctx, cookieID, itemID)
	if err != nil {
		return ItemAIPrompt{}, err
	}
	if !exists {
		return ItemAIPrompt{}, ErrAIPromptNotFound
	}
	prompt.CookieID, prompt.ItemID = cookieID, itemID
	// encoded 是以 JSON 文本形式存储的自定义变量，err 表示变量映射无法序列化，此时保存必须中止以免写出半成品配置。
	encoded, err := json.Marshal(prompt.CustomVariables)
	if err != nil {
		return ItemAIPrompt{}, fmt.Errorf("自定义变量编码失败: %w", err)
	}
	// stored 是交给仓储的持久化副本：清空明文映射，仅保留 JSON 文本列，避免同一份变量被双重写入。
	stored := prompt
	stored.CustomVariables = nil
	stored.CustomVariablesJSON = string(encoded)
	// err 是持久化写入失败的错误，直接返回给上层，调用方据此返回 5xx 而不是成功响应。
	if err := service.repository.SaveAIPrompt(ctx, stored); err != nil {
		return ItemAIPrompt{}, err
	}
	return prompt, nil
}

// validateAIPromptTarget 校验用户、账号和商品标识的基本格式。
func validateAIPromptTarget(userID int64, cookieID, itemID string) (string, string, error) {
	cookieID, itemID = strings.TrimSpace(cookieID), strings.TrimSpace(itemID)
	if userID <= 0 || cookieID == "" || itemID == "" {
		return "", "", ErrAIPromptInvalid
	}
	return cookieID, itemID, nil
}

// validateAIPromptInput 校验策略、正文长度和变量数量、键值长度。
func validateAIPromptInput(prompt ItemAIPrompt) error {
	if prompt.Strategy != "inherit" && prompt.Strategy != "append" && prompt.Strategy != "override" {
		return fmt.Errorf("%w：strategy 必须是 inherit、append 或 override", ErrAIPromptInvalid)
	}
	if len(prompt.CustomVariables) > 32 {
		return fmt.Errorf("%w：自定义变量最多 32 个", ErrAIPromptInvalid)
	}
	// key 是自定义变量名（不允许与内置变量重名），value 是其渲染时的替换文本；两者长度上限分别为 64 与 1000 个字符。
	for key, value := range prompt.CustomVariables {
		if key == "item_title" || key == "item_description" || key == "item_price" || key == "item_category" || key == "chat_history" {
			return fmt.Errorf("%w：自定义变量名 %q 为内置名称", ErrAIPromptInvalid, key)
		}
		if utf8.RuneCountInString(key) == 0 || utf8.RuneCountInString(key) > 64 || utf8.RuneCountInString(value) > 1000 {
			return fmt.Errorf("%w：自定义变量键值长度超限", ErrAIPromptInvalid)
		}
	}
	return nil
}

// decodeAIPrompt 将仓储 JSON 模型转换为应用模型并提供默认空对象。
func decodeAIPrompt(prompt ItemAIPrompt) (ItemAIPrompt, error) {
	// variables 是解码后的自定义变量映射，始终非 nil，保证调用方无需处理 nil map。
	variables := make(map[string]string)
	if strings.TrimSpace(prompt.CustomVariablesJSON) != "" {
		// err 是仓储 JSON 文本无法反序列化为字符串映射的错误，此时返回错误而不是静默丢弃变量。
		if err := json.Unmarshal([]byte(prompt.CustomVariablesJSON), &variables); err != nil {
			return ItemAIPrompt{}, fmt.Errorf("读取自定义变量失败: %w", err)
		}
	}
	prompt.CustomVariables = variables
	prompt.CustomVariablesJSON = ""
	return prompt, nil
}
