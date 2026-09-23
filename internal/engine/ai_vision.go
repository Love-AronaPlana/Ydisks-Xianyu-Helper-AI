package engine

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xianyu-go/internal/netguard"
)

// aiVisionDownloadTimeout 是下载单张买家图片的墙钟上限；图片下载属于回复路径上的额外等待，必须短于模型的 30 秒调用预算。
const aiVisionDownloadTimeout = 15 * time.Second

// aiVisionMaxImageBytes 是单张买家图片允许送入模型的最大字节数，超过则跳过该图而不阻断回复。
const aiVisionMaxImageBytes = 4 << 20

// aiVisionMaxTotalBytes 是一条消息内全部图片允许送入模型的字节总量上限。
const aiVisionMaxTotalBytes = 12 << 20

// aiVisionAllowedImageTypes 是允许送入模型的多模态图片类型；不接受 SVG 等可能携带脚本的类型。
var aiVisionAllowedImageTypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
	"image/gif":  {},
}

// newAIVisionHTTPClient 构造下载买家图片使用的受限 HTTP 客户端。
// 它跟随系统出站策略（含内网限制与响应体上限），并在测试中可被整体替换以避免真实出网。
var newAIVisionHTTPClient = func() *http.Client {
	return netguard.ConfiguredHTTPClient(aiVisionDownloadTimeout)
}

// downloadAIImageDataURI 下载一张买家图片并编码为多模态模型可直接消费的 data URI。
// 返回值只包含图片内容，不含 Cookie 或凭证；调用方不得把返回值或 imageURL 写入日志。
func downloadAIImageDataURI(ctx context.Context, imageURL string) (string, error) {
	// parsedURL、parseErr 是图片地址的解析结果与失败原因；只接受无凭证的公网协议地址。
	parsedURL, parseErr := url.Parse(strings.TrimSpace(imageURL))
	if parseErr != nil || parsedURL.Host == "" {
		return "", fmt.Errorf("买家图片地址无效")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("买家图片地址必须是 http 或 https")
	}
	if parsedURL.User != nil {
		return "", fmt.Errorf("买家图片地址不允许包含凭证")
	}
	// request 是带调用方 Context 的下载请求，取消必须立刻中止。
	request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if requestErr != nil {
		return "", fmt.Errorf("构造买家图片请求失败: %w", requestErr)
	}
	// response 是图片服务响应；非 2xx 视为不可用，不做重试以免放大外部请求。
	response, responseErr := newAIVisionHTTPClient().Do(request)
	if responseErr != nil {
		return "", fmt.Errorf("下载买家图片失败: %w", responseErr)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("下载买家图片失败: 状态 %d", response.StatusCode)
	}
	// data、readErr 是受限读取到的图片字节与读取错误；超限时按超限处理而不是截断。
	data, readErr := io.ReadAll(io.LimitReader(response.Body, aiVisionMaxImageBytes+1))
	if readErr != nil {
		return "", fmt.Errorf("读取买家图片失败: %w", readErr)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("买家图片内容为空")
	}
	if len(data) > aiVisionMaxImageBytes {
		return "", fmt.Errorf("买家图片超过 %d MiB", aiVisionMaxImageBytes>>20)
	}
	// contentType 是归一化后的图片类型；服务端未声明或声明为通用二进制时按实际字节嗅探。
	contentType := normalizeAIVisionContentType(response.Header.Get("Content-Type"), data)
	// allowed 表示归一化类型是否在多模态白名单内；不在白名单（例如可能携带脚本的 SVG）时整张图片拒绝，不降级为二进制发送。
	if _, allowed := aiVisionAllowedImageTypes[contentType]; !allowed {
		return "", fmt.Errorf("买家图片类型 %s 不受支持", contentType)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// normalizeAIVisionContentType 归一化图片响应类型并补齐缺失类型。
// rawHeader 是响应头原值；data 是用于 Content-Type 嗅探的图片字节。
func normalizeAIVisionContentType(rawHeader string, data []byte) string {
	// contentType 是去掉参数和空白后的声明类型。
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(rawHeader, ";")[0]))
	if contentType == "" || contentType == "application/octet-stream" {
		return strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	}
	return contentType
}

// collectAIVisionImages 按顺序下载当前消息中的买家图片，并返回可直接放入模型请求的 data URI 列表。
// 单张图片失败只跳过该图；返回空切片表示没有任何可用图片，调用方应退回纯文本请求。
func (a *AIReplierImpl) collectAIVisionImages(ctx context.Context, imageURLs []string) []string {
	// images 保存成功下载并编码的图片，保持消息中的原始顺序，供模型按买家上传次序理解语义。
	images := make([]string, 0, len(imageURLs))
	// failed 统计因下载失败或超出单条消息总量上限而被跳过的图片数量，仅用于非敏感诊断日志，不参与回复内容。
	failed := 0
	// totalBytes 累计已接受图片的原始字节，用于执行整条消息的总量上限。
	totalBytes := 0
	// imageURL 是当前待下载的买家图片地址。
	for _, imageURL := range imageURLs {
		if strings.TrimSpace(imageURL) == "" {
			continue
		}
		// dataURI、err 是单张图片的编码结果与失败原因；失败原因不包含图片地址。
		dataURI, err := downloadAIImageDataURI(ctx, imageURL)
		if err != nil {
			failed++
			a.logger.Warn("跳过无法识别的买家图片", "reason", err)
			continue
		}
		// estimate 是 data URI 对应的原始字节估算值，用于总量控制。
		estimate := len(dataURI) * 3 / 4
		if totalBytes+estimate > aiVisionMaxTotalBytes {
			failed++
			continue
		}
		totalBytes += estimate
		images = append(images, dataURI)
	}
	if failed > 0 {
		a.logger.Info("买家图片部分可用", "accepted", len(images), "skipped", failed)
	}
	return images
}
