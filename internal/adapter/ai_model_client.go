package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	settingsapp "xianyu-go/internal/application/settings"
	"xianyu-go/internal/netguard"
)

// MaxAIModelsResponseBytes 限制远端模型目录响应大小，避免管理员配置的端点耗尽内存。
const MaxAIModelsResponseBytes = 4 << 20

// AIModelClient 通过管理员明确配置的端点读取 AI 模型目录。
type AIModelClient struct {
	// newHTTPClient 允许测试替换端点客户端，同时生产默认使用受信任端点策略。
	newHTTPClient func(baseURL string) (*http.Client, error)
	// newTestHTTPClient 允许测试连接使用更长的超时，适应慢推理模型。
	newTestHTTPClient func(baseURL string) (*http.Client, error)
}

// NewAIModelClient 构造 AI 模型目录适配器。
func NewAIModelClient() *AIModelClient {
	// client 保存生产环境使用的模型目录客户端。
	client := &AIModelClient{
		newHTTPClient: func(baseURL string) (*http.Client, error) {
			return netguard.ConfiguredEndpointHTTPClient(baseURL, 20*time.Second)
		},
		newTestHTTPClient: func(baseURL string) (*http.Client, error) {
			return netguard.ConfiguredEndpointHTTPClient(baseURL, 50*time.Second)
		},
	}
	return client
}

// Fetch 请求模型目录并只返回模型名称，不返回 API 密钥或原始响应内容。
func (c *AIModelClient) Fetch(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("AI API 地址为空")
	}
	if c == nil || c.newHTTPClient == nil {
		return nil, fmt.Errorf("AI 模型客户端未初始化")
	}
	// req 是带有请求上下文的模型目录请求；API 密钥只存在于请求头。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	// client 是经过地址校验的出站客户端。
	client, err := c.newHTTPClient(baseURL)
	if err != nil {
		return nil, fmt.Errorf("AI API 地址无效: %w", err)
	}
	// response 是远端模型目录 HTTP 响应。
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取模型失败: %w", err)
	}
	defer response.Body.Close()
	// raw 是限制大小后读取的响应内容。
	raw, err := ReadAIModelsBody(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("读取模型失败: HTTP %d %s", response.StatusCode, truncateAIModelBody(string(raw), 180))
	}
	// models、err 保存解析后的模型名称及解析错误。
	models, err := ParseAIModels(raw)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("模型列表为空")
	}
	return models, nil
}

// ReadAIModelsBody 读取并限制远端模型目录响应。
func ReadAIModelsBody(reader io.Reader) ([]byte, error) {
	// raw 是限制大小后读取的响应内容。
	raw, err := io.ReadAll(io.LimitReader(reader, MaxAIModelsResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxAIModelsResponseBytes {
		return nil, fmt.Errorf("模型列表响应超过 %d MiB", MaxAIModelsResponseBytes>>20)
	}
	return raw, nil
}

// ParseAIModels 从兼容 OpenAI 的多种响应形状提取去重模型名称。
func ParseAIModels(raw []byte) ([]string, error) {
	// payload 是远端模型目录的通用 JSON 载荷。
	var payload any
	// err 表示远端模型目录 JSON 解析错误。
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}
	// seen 保存已返回的模型名称，避免重复项污染下拉列表。
	seen := make(map[string]bool)
	// result 保存按远端出现顺序排列的模型名称。
	var result []string
	// addModel 将非空模型名称加入结果并去重。
	addModel := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		result = append(result, value)
	}
	// walk 递归解析 data、models、对象和字符串等兼容响应结构。
	var walk func(any)
	walk = func(value any) {
		// typed 是当前 JSON 节点的具体类型。
		switch typed := value.(type) {
		case []any:
			// item 是模型目录数组中的当前元素。
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			// id、ok 保存优先使用的模型标识及其类型判断结果。
			if id, ok := typed["id"].(string); ok && id != "" {
				addModel(id)
			} else if // name、ok 保存模型名称回退值及其类型判断结果。
			name, ok := typed["name"].(string); ok && name != "" {
				addModel(name)
			}
		case string:
			addModel(typed)
		}
	}
	// root、ok 保存顶层对象及对象类型判断结果。
	if root, ok := payload.(map[string]any); ok {
		// data、ok 保存兼容 OpenAI 的 data 字段及存在性判断结果。
		if data, ok := root["data"]; ok {
			walk(data)
		} else if // models、ok 保存兼容服务的 models 字段及存在性判断结果。
		models, ok := root["models"]; ok {
			walk(models)
		}
	} else {
		walk(payload)
	}
	return result, nil
}

// AIConnectionTestResult 描述一次 AI 连接测试的诊断信息。
// 返回 application 层的 AIConnectionTestResult 以满足 ModelClient 端口契约。

// TestConnection 发送一次最小 chat completion 请求，验证 API 地址、密钥和模型的组合是否可用。
// API 密钥只存在于请求头中，不会被记录或返回。
func (c *AIModelClient) TestConnection(ctx context.Context, baseURL, apiKey, model string) (settingsapp.AIConnectionTestResult, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return settingsapp.AIConnectionTestResult{}, fmt.Errorf("AI API 地址为空")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return settingsapp.AIConnectionTestResult{}, fmt.Errorf("未选择模型，请先填写模型名称或点击「读取模型」")
	}
	if c == nil || c.newHTTPClient == nil {
		return settingsapp.AIConnectionTestResult{}, fmt.Errorf("AI 模型客户端未初始化")
	}
	// testClient 使用 50 秒超时，适应推理类模型的首 token 延迟。
	var client *http.Client
	var clientErr error
	if c.newTestHTTPClient != nil {
		client, clientErr = c.newTestHTTPClient(baseURL)
	} else {
		// 退化到默认客户端，保证旧测试不需要额外装配。
		client, clientErr = c.newHTTPClient(baseURL)
	}
	if clientErr != nil {
		return settingsapp.AIConnectionTestResult{}, fmt.Errorf("AI API 地址无效: %w", clientErr)
	}
	// 用 json.Marshal 构造请求体，确保模型名中的特殊字符被正确转义。
	payload := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "你好"}},
		"max_tokens": 16,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return settingsapp.AIConnectionTestResult{}, fmt.Errorf("构造测试请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return settingsapp.AIConnectionTestResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}

	start := time.Now()
	response, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return settingsapp.AIConnectionTestResult{Model: model, LatencyMS: latency}, fmt.Errorf("连接失败: %w", err)
	}
	defer response.Body.Close()

	raw, err := ReadAIModelsBody(response.Body)
	if err != nil {
		return settingsapp.AIConnectionTestResult{}, err
	}

	if response.StatusCode == 401 || response.StatusCode == 403 {
		return settingsapp.AIConnectionTestResult{Model: model, LatencyMS: latency}, fmt.Errorf("认证失败：API Key 无效或权限不足 (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode == 404 {
		return settingsapp.AIConnectionTestResult{Model: model, LatencyMS: latency}, fmt.Errorf("模型不存在或地址错误 (HTTP 404)")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return settingsapp.AIConnectionTestResult{Model: model, LatencyMS: latency}, fmt.Errorf("请求失败: HTTP %d %s", response.StatusCode, truncateAIModelBody(string(raw), 180))
	}

	reply := extractChatReply(raw)
	if reply == "" {
		return settingsapp.AIConnectionTestResult{Model: model, LatencyMS: latency}, fmt.Errorf("请求成功但未返回有效回复内容")
	}
	return settingsapp.AIConnectionTestResult{Model: model, LatencyMS: latency, Reply: truncateAIModelBody(reply, 100)}, nil
}

// extractChatReply 从 OpenAI 兼容的 chat completion 响应中提取模型回复文本。
func extractChatReply(raw []byte) string {
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if len(payload.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(payload.Choices[0].Message.Content)
}

// truncateAIModelBody 截取文本，避免把超大远端正文写入日志或 HTTP 错误。
// 按 rune 截断，防止切出半个多字节字符。
func truncateAIModelBody(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

var _ interface {
	Fetch(context.Context, string, string) ([]string, error)
	TestConnection(context.Context, string, string, string) (settingsapp.AIConnectionTestResult, error)
} = (*AIModelClient)(nil)
