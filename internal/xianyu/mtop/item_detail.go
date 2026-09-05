// Package mtop: 商品详情域 — 补充商品列表未返回的多规格信息。
package mtop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xianyu-go/internal/xianyu/protocol"
)

// ItemDetailFetcher 是商品同步使用的可选详情能力。
type ItemDetailFetcher interface {
	DetectItemMultiSpec(ctx context.Context, cookies, itemID string) (bool, error)
}

var _ ItemDetailFetcher = (*ClientImpl)(nil)

// DetectItemMultiSpec 查询商品详情并识别多规格结构；MTOP 签名 Token 过期时刷新后重试。
// 当 ctx 携带 CookieSession 时会像浏览器一样吸收响应 Cookie。
func (c *ClientImpl) DetectItemMultiSpec(ctx context.Context, cookies, itemID string) (bool, error) {
	// currentCookies 保存本轮商品详情请求实际使用的 Cookie，不向日志或错误输出。
	currentCookies := cookies
	if // session 用于吸收没有显式会话调用方收到的 Set-Cookie。
	session := cookieSessionFromContext(ctx); session != nil {
		currentCookies, _, _ = session.State()
	} else {
		ctx, _ = WithFlatCookieSession(ctx, currentCookies)
	}
	for // attempt 表示含首次请求在内的 Token 重试序号。
	attempt := 0; attempt < 4; attempt++ {
		// previousCookies 记录本次请求前的 Cookie，用于优先采用响应下发的 Token。
		previousCookies := currentCookies
		// ok、err 保存一次商品详情请求结果。
		ok, err := c.detectItemMultiSpecOnce(ctx, currentCookies, itemID)
		if err == nil {
			return ok, nil
		}
		if !IsMTopTokenExpiredErr(err) {
			return false, err
		}
		if // session 用于读取响应刚吸收的最新 Cookie。
		session := cookieSessionFromContext(ctx); session != nil {
			currentCookies, _, _ = session.State()
		}
		if attempt == 3 {
			return false, fmt.Errorf("商品详情接口 Token 重试失败: %w", err)
		}
		if currentCookies == previousCookies {
			// refreshed、refreshErr 保存主动刷新 MTOP 签名 Token 的结果及错误。
			refreshed, refreshErr := c.RefreshTokenContext(ctx, currentCookies)
			if refreshErr != nil {
				return false, fmt.Errorf("商品详情 Token 过期且刷新失败: %w", refreshErr)
			}
			currentCookies = refreshed.UpdatedCookies
		}
		if // sleepErr 表示两次平台请求之间等待期间的取消错误。
		sleepErr := sleepCtx(ctx, MTopRetryGap); sleepErr != nil {
			return false, sleepErr
		}
	}
	return false, errors.New("商品详情 Token 重试失败")
}

// detectItemMultiSpecOnce 执行一次商品详情请求；调用方负责 Token 过期后的恢复重试。
func (c *ClientImpl) detectItemMultiSpecOnce(ctx context.Context, cookies, itemID string) (bool, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return false, fmt.Errorf("item_id 不能为空")
	}
	// endpoint 用于本次流程后续判断的endpoint
	endpoint := c.ItemDetailURL
	if endpoint == "" {
		endpoint = ItemDetailAPI
	}
	// documentURL 用于本次流程后续判断的documentURL
	documentURL := "https://www.goofish.com/item?id=" + url.QueryEscape(itemID)
	// signingCookies、requestCookies 用于本次流程后续判断的signingCookies、requestCookies
	signingCookies, requestCookies := mtopRequestCookies(ctx, cookies, documentURL, endpoint)
	// token 用于本次流程后续判断的令牌
	token := protocol.SignToken(signingCookies)
	if token == "" {
		return false, fmt.Errorf("cookie 缺少 _m_h5_tk，无法获取商品详情")
	}
	// dataVal 用于本次流程后续判断的数据Val
	dataVal := `{"itemId":` + strconv.Quote(itemID) + `}`
	// timestamp 用于本次流程后续判断的timestamp
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	// sign 用于本次流程后续判断的sign
	sign := protocol.GenerateSign(timestamp, token, dataVal)
	// req、err 用于本次流程后续判断的req、err
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?"+buildItemDetailQuery(timestamp, sign), strings.NewReader("data="+url.QueryEscape(dataVal)))
	if err != nil {
		return false, err
	}
	setCommonHeaders(req, requestCookies)
	req.Header.Set("Origin", "https://www.goofish.com")
	req.Header.Set("Referer", documentURL)

	// hc 用于本次流程后续判断的hc
	hc := c.httpClient()
	// resp、err 用于本次流程后续判断的resp、err
	resp, err := hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("商品详情请求失败: %w", err)
	}
	defer resp.Body.Close()
	absorbMTopResponseCookies(ctx, cookies, resp)
	// raw、err 用于本次流程后续判断的raw、err
	raw, err := readMTopBody(resp)
	if err != nil {
		return false, c.mtopResponseFailure("商品详情接口", resp.StatusCode, nil, fmt.Sprintf("读取响应失败: %v", err))
	}
	// decoded 用于本次流程后续判断的decoded
	var decoded struct {
		Ret  []string       `json:"ret"`
		Data map[string]any `json:"data"`
	}
	if // err 用于本次流程后续判断的err
	err := json.Unmarshal(raw, &decoded); err != nil {
		return false, c.mtopResponseFailure("商品详情接口", resp.StatusCode, nil, fmt.Sprintf("JSON 解析失败: %v", err))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, c.mtopResponseFailure("商品详情接口", resp.StatusCode, decoded.Ret, "HTTP 状态异常")
	}
	if !hasMTopSuccess(decoded.Ret) {
		return false, c.mtopResponseFailure("商品详情接口", resp.StatusCode, decoded.Ret, "平台 ret 未包含 SUCCESS")
	}
	return detectItemMultiSpec(decoded.Data), nil
}

// buildItemDetailQuery 封装build商品Detail查询业务协调。
func buildItemDetailQuery(timestamp, sign string) string {
	// values 用于本次流程后续判断的values
	values := url.Values{
		"jsv":           {"2.7.2"},
		"appKey":        {protocol.SignAppKey},
		"t":             {timestamp},
		"sign":          {sign},
		"v":             {"1.0"},
		"type":          {"originaljson"},
		"accountSite":   {"xianyu"},
		"dataType":      {"json"},
		"timeout":       {"20000"},
		"api":           {"mtop.taobao.idle.pc.detail"},
		"sessionOption": {"AutoLoginOnly"},
		"spm_cnt":       {"a21ybx.item.0.0"},
	}
	return values.Encode()
}

// detectItemMultiSpec 封装detect商品MultiSpec业务协调。
func detectItemMultiSpec(value any) bool {
	return detectMultiSpecValue(value, false, 0)
}

// detectMultiSpecValue 封装detectMultiSpec值业务协调。
func detectMultiSpecValue(value any, skuContext bool, depth int) bool {
	if depth > 16 {
		return false
	}
	switch // typed 用于本次流程后续判断的typed
	typed := value.(type) {
	case map[string]any:
		// key、child 表示当前遍历过程中的key、child
		for key, child := range typed {
			// normalized 用于本次流程后续判断的normalized
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normalized {
			case "multisku", "ismultisku", "ismultispec", "multiplesku":
				if mtopBool(child) {
					return true
				}
			case "skulist", "skus":
				if // list、ok 用于本次流程后续判断的list、ok
				list, ok := child.([]any); ok && len(list) > 1 {
					return true
				}
			case "skuprops", "skuproperties", "specprops", "specifications":
				if // list、ok 用于本次流程后续判断的list、ok
				list, ok := child.([]any); ok && len(list) > 0 {
					return true
				}
			case "props", "properties":
				if skuContext {
					if // list、ok 用于本次流程后续判断的list、ok
					list, ok := child.([]any); ok && len(list) > 0 {
						return true
					}
				}
			}
			// nextSKUContext 用于本次流程后续判断的nextSKU上下文
			nextSKUContext := skuContext || normalized == "skudo" || normalized == "skubase" || normalized == "skumodel"
			if detectMultiSpecValue(child, nextSKUContext, depth+1) {
				return true
			}
		}
	case []any:
		// child 表示当前遍历过程中的child
		for _, child := range typed {
			if detectMultiSpecValue(child, skuContext, depth+1) {
				return true
			}
		}
	}
	return false
}
