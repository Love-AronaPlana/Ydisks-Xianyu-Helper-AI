package engine

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

// replyDimensionPNG 生成本地 HTTP 服务使用的 PNG；t 管理失败，width/height 是目标像素尺寸。
func replyDimensionPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	// source 是用于编码的已知尺寸测试图片。
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	// pixel 是测试图片填充使用的固定颜色。
	pixel := color.RGBA{R: 20, G: 80, B: 160, A: 255}
	// y 表示当前填充行。
	for y := 0; y < height; y++ {
		// x 表示当前填充列。
		for x := 0; x < width; x++ {
			source.SetRGBA(x, y, pixel)
		}
	}
	// buffer 保存内存中的 PNG 编码结果。
	var buffer bytes.Buffer
	// encodeErr 保存将测试图片编码为 PNG 时的错误。
	if encodeErr := png.Encode(&buffer, source); encodeErr != nil {
		t.Fatalf("编码测试 PNG 失败: %v", encodeErr)
	}
	return buffer.Bytes()
}

// TestResolveReplyImageDimensionsWithLocalHTTP 验证图片头部解析能返回原始像素宽高；t 管理本地服务和断言。
func TestResolveReplyImageDimensionsWithLocalHTTP(t *testing.T) {
	// imageData 保存尺寸为 1600×900 的确定性 PNG 内容。
	imageData := replyDimensionPNG(t, 1600, 900)
	// server 返回本地图片内容；writer 写响应，忽略的请求对象不参与 fixture 判定。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(imageData)
	}))
	defer server.Close()
	// width、height 和 resolveErr 保存解析出的宽高及本地请求错误。
	width, height, resolveErr := resolveReplyImageDimensionsWithClient(context.Background(), server.URL+"/image.png", server.Client())
	if resolveErr != nil || width != 1600 || height != 900 {
		t.Fatalf("图片宽高=%dx%d err=%v", width, height, resolveErr)
	}
}

// TestResolveReplyImageDimensionsRejectsInvalidInputs 验证无效地址、客户端和响应均被拒绝；t 管理子测试。
func TestResolveReplyImageDimensionsRejectsInvalidInputs(t *testing.T) {
	// imageData 是用于生成可访问本地图片响应的确定性 PNG 内容。
	imageData := replyDimensionPNG(t, 4, 3)
	// server 提供成功图片、错误状态和无效图片三类响应；writer 写响应，request 选择对应 fixture。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/image.png":
			writer.Header().Set("Content-Type", "image/png")
			_, _ = writer.Write(imageData)
		case "/error":
			http.Error(writer, "unavailable", http.StatusBadGateway)
		default:
			_, _ = writer.Write([]byte("not an image"))
		}
	}))
	defer server.Close()
	// cases 保存需要拒绝的输入组合及其本地 HTTP 客户端。
	cases := []struct {
		// name 是当前拒绝场景的测试名称。
		name string
		// imageURL 是待解析的地址或本地响应路径。
		imageURL string
		// client 控制是否可发起本地读取。
		client *http.Client
	}{
		{name: "unsupported-scheme", imageURL: "ftp://images.example/photo.png", client: server.Client()},
		{name: "credentials-in-url", imageURL: "http://fixture@images.example/photo.png", client: server.Client()},
		{name: "missing-client", imageURL: server.URL + "/image.png"},
		{name: "http-failure", imageURL: server.URL + "/error", client: server.Client()},
		{name: "invalid-image", imageURL: server.URL + "/invalid", client: server.Client()},
	}
	// testCase 表示当前需要验证的拒绝输入。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// width、height 和 resolveErr 保存拒绝结果的尺寸及错误。
			width, height, resolveErr := resolveReplyImageDimensionsWithClient(context.Background(), testCase.imageURL, testCase.client)
			if resolveErr == nil || width != 0 || height != 0 {
				t.Fatalf("无效输入未被拒绝，宽高=%dx%d err=%v", width, height, resolveErr)
			}
		})
	}
}

// TestResolveReplyImageDimensionsHonorsCancellation 验证图片尺寸请求沿用调用方取消上下文；t 管理本地服务和断言。
func TestResolveReplyImageDimensionsHonorsCancellation(t *testing.T) {
	// server 的 request 等待客户端取消，writer 在本场景不写响应内容。
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	// ctx 和 cancel 控制本地图片请求的生命周期。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// width、height 和 resolveErr 保存取消请求的尺寸及错误。
	width, height, resolveErr := resolveReplyImageDimensionsWithClient(ctx, server.URL+"/image.png", server.Client())
	if resolveErr == nil || width != 0 || height != 0 {
		t.Fatalf("已取消请求应停止解析，宽高=%dx%d err=%v", width, height, resolveErr)
	}
}
