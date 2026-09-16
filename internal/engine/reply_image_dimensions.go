package engine

import (
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xianyu-go/internal/netguard"
)

const (
	// replyImageDimensionTimeout 限制自动回复读取图片头部的最长等待时间，减少坏链路对回复时延的影响。
	replyImageDimensionTimeout = 2 * time.Second
	// replyImageHeaderLimit 限制图片头部解析读取的字节数，避免畸形图片拖长回复任务。
	replyImageHeaderLimit = 1 << 20
)

// replyImageDimensionResolver 读取图片像素尺寸；ctx 控制网络取消，imageURL 是待读取地址；width/height 单位为像素，err 表示应沿用 WebSocket 协议默认尺寸。
type replyImageDimensionResolver func(ctx context.Context, imageURL string) (width, height int, err error)

// resolveReplyImageDimensions 通过公网安全客户端读取图片元数据，不下载或持久化完整图片。
// ctx 是回复流程的取消边界；imageURL 是待识别的图片地址；返回宽高单位为像素，err 表示读取失败。
func resolveReplyImageDimensions(ctx context.Context, imageURL string) (int, int, error) {
	return resolveReplyImageDimensionsWithClient(ctx, imageURL, netguard.PublicHTTPClient(replyImageDimensionTimeout))
}

// resolveReplyImageDimensionsWithClient 校验图片地址并解析响应中的宽高；client 仅用于本地确定性测试注入。
// ctx 控制本次请求取消，imageURL 是来源图片地址；返回宽高单位为像素，err 表示地址、网络或图片数据无效。
func resolveReplyImageDimensionsWithClient(ctx context.Context, imageURL string, client *http.Client) (int, int, error) {
	// parsedURL 和 parseErr 保存图片地址解析结果及格式错误。
	parsedURL, parseErr := url.Parse(strings.TrimSpace(imageURL))
	if parseErr != nil || parsedURL.Hostname() == "" || parsedURL.User != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return 0, 0, errors.New("图片地址无效")
	}
	if client == nil {
		return 0, 0, errors.New("图片读取客户端未配置")
	}
	// request 和 requestErr 保存受调用方取消控制的图片头部请求及构造错误。
	request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if requestErr != nil {
		return 0, 0, requestErr
	}
	// response 和 requestErr 保存图片服务响应及网络错误。
	response, requestErr := client.Do(request)
	if requestErr != nil {
		return 0, 0, requestErr
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return 0, 0, errors.New("图片服务返回非成功状态")
	}
	// config、format 和 decodeErr 保存图片解码器识别出的尺寸、格式及头部解析错误。
	config, format, decodeErr := image.DecodeConfig(io.LimitReader(response.Body, replyImageHeaderLimit))
	if decodeErr != nil {
		return 0, 0, decodeErr
	}
	if config.Width <= 0 || config.Height <= 0 || format == "" {
		return 0, 0, image.ErrFormat
	}
	return config.Width, config.Height, nil
}
