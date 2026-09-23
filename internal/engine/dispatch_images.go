package engine

import (
	"encoding/json"
	"strings"
)

// 本文件承载买家图片消息的解析与放行判定；它们只服务于多模态 AI 上下文，
// 与文本消息分发解耦，避免入站消息解析文件继续膨胀。

// buyerImagePlaceholder 是纯图片消息在缺少可读摘要时使用的正文占位，与闲鱼自身对图片消息的展示一致。
const buyerImagePlaceholder = "[图片]"

// buyerImageMaxURLs 限制单条买家消息送入模型的图片数量，避免多图消息放大请求体与模型费用。
const buyerImageMaxURLs = 3

// extractBuyerImageURLs 解析买家图片消息中的图片地址。
// 只有平台在 extJson 中声明 contentType=2 时才读取 image.pics[].url，避免把商品卡片或提醒图误当成买家上传；
// 返回最多 buyerImageMaxURLs 个去重后的 http(s) 地址，调用方可直接交给多模态模型下载。
func extractBuyerImageURLs(m1, m10 map[string]any, decrypted map[string]any) []string {
	// 非图片消息一律不解析图片，防止把交易卡片或商品主图当作买家上传内容。
	if messageContentType(m1, m10) != "2" {
		return nil
	}

	// out 保存按出现顺序收集的买家图片地址，最多 buyerImageMaxURLs 个。
	out := make([]string, 0, buyerImageMaxURLs)
	// seen 记录已收集的图片地址；同一张图在协议中重复出现时只保留首次，避免重复下载与重复计入模型费用。
	seen := make(map[string]struct{}, buyerImageMaxURLs)
	// walk 递归展开平台对象、数组和嵌套 JSON 字符串，兼容不同客户端版本的封装层级。
	var walk func(any)
	walk = func(current any) {
		if current == nil || len(out) >= buyerImageMaxURLs {
			return
		}
		// typed 保存当前节点的具体协议类型，便于继续展开嵌套图片正文。
		switch typed := current.(type) {
		case string:
			// nested 保存可能以字符串封装的图片消息对象。
			var nested any
			if json.Unmarshal([]byte(typed), &nested) == nil {
				walk(nested)
			}
		case map[string]any:
			// image 是协议声明的图片正文对象；断言成功才继续读取其中的图片数组。
			if image, imageFound := typed["image"].(map[string]any); imageFound {
				// pics、picsFound 是图片数组及其断言结果；字段缺失或类型不符时按无图处理，不中断整条消息解析。
				if pics, picsFound := image["pics"].([]any); picsFound {
					// picValue 是图片数组中的单个协议节点，只需读取其中的 url 字段。
					for _, picValue := range pics {
						if len(out) >= buyerImageMaxURLs {
							return
						}
						// pic 是当前图片字段；断言失败时按空对象处理，地址读取会得到空串并在下面被跳过。
						pic, _ := picValue.(map[string]any)
						// picURL 是去掉首尾空白后的图片公网地址，只有 http(s) 地址才会进入模型请求。
						picURL := strings.TrimSpace(toString(pic["url"]))
						if !strings.HasPrefix(picURL, "http://") && !strings.HasPrefix(picURL, "https://") {
							continue
						}
						// exists 表示该地址是否已被收集；重复图片直接跳过，保证每张图只发送一次。
						if _, exists := seen[picURL]; exists {
							continue
						}
						seen[picURL] = struct{}{}
						out = append(out, picURL)
					}
				}
			}
			// child 保存当前对象的嵌套字段，继续寻找不同客户端版本的图片路径。
			for _, child := range typed {
				walk(child)
			}
		case []any:
			// child 保存数组中的协议节点，按平台原始顺序递归展开。
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(decrypted)
	return out
}

// isBuyerImageMessage 判断已解析出图片的帧是否确实来自真实买家会话。
// 系统提示发送者（1400）和非单聊会话继续按原规则丢弃，只对买家本人上传的图片放行。
func isBuyerImageMessage(m1, m10 map[string]any, imageCount int) bool {
	if imageCount == 0 {
		return false
	}
	if strings.TrimSuffix(strings.TrimSpace(toString(m10["senderUserId"])), "@goofish") == "1400" {
		return false
	}
	// sessionType 为空表示旧客户端未提供该字段，按单聊放行；显式非 "1" 仍视为通知会话。
	if sessionType := strings.TrimSpace(toString(m10["sessionType"])); sessionType != "" && sessionType != "1" {
		return false
	}
	return true
}

// extractImageObservationContent 从自身回显中提取第一张图片 URL，供自动化发送确认使用。
// value 是已解密但未包含凭证的 WebSocket 消息；解析失败或缺少公网 URL 时返回空值。
func extractImageObservationContent(value any) string {
	// imageURL 保存当前回显中首个可比较的图片地址；找到后停止递归，保持消息顺序稳定。
	var imageURL string
	// walk 递归展开平台对象、数组和嵌套 JSON 字符串，避免依赖单一客户端版本的固定路径。
	var walk func(any)
	walk = func(current any) {
		if imageURL != "" || current == nil {
			return
		}
		// typed 保存当前节点的具体协议类型，便于继续展开嵌套图片正文。
		switch typed := current.(type) {
		case string:
			// nested 保存可能作为字符串封装的图片消息对象。
			var nested any
			if json.Unmarshal([]byte(typed), &nested) == nil {
				walk(nested)
			}
		case map[string]any:
			// image 保存平台图片消息的对象；pics 保存其中按发送顺序排列的图片数组。
			// imageFound 表示当前对象是否包含图片正文；picsFound 表示是否找到图片数组。
			if image, imageFound := typed["image"].(map[string]any); imageFound {
				// pics、picsFound 保存图片数组及其存在性，避免把非图片节点误当作可确认正文。
				if pics, picsFound := image["pics"].([]any); picsFound {
					// picValue 保存图片数组中的单个协议节点，后续只读取其中的 URL 字段。
					for _, picValue := range pics {
						// pic 保存当前图片字段；picURL 是其可直接比较的 URL。
						pic, _ := picValue.(map[string]any)
						// picURL 保存当前图片的公网地址，用于和发送参数做幂等匹配。
						picURL := strings.TrimSpace(toString(pic["url"]))
						if strings.HasPrefix(picURL, "http://") || strings.HasPrefix(picURL, "https://") {
							imageURL = picURL
							return
						}
					}
				}
			}
			// child 保存当前对象的嵌套字段，继续寻找不同客户端版本的图片路径。
			for _, child := range typed {
				walk(child)
			}
		case []any:
			// child 保存数组中的协议节点，按平台原始顺序递归展开。
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return imageURL
}
