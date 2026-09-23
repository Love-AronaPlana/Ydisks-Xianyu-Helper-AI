package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buyerImageFrame 构造一条平台声明为图片（contentType=2）的买家消息帧。
func buyerImageFrame(imageURLs []string, reminder string) map[string]any {
	// pics 按平台协议排列图片数组，供递归解析提取 url 字段。
	pics := make([]any, 0, len(imageURLs))
	// imageURL 是当前待写入的图片地址。
	for _, imageURL := range imageURLs {
		pics = append(pics, map[string]any{"url": imageURL})
	}
	// contentJSON 是平台图片正文，contentType=2 表示图片消息。
	contentJSON, _ := json.Marshal(map[string]any{"contentType": 2, "image": map[string]any{"pics": pics}})
	return map[string]any{
		"1": map[string]any{
			"2": "chat-1@goofish",
			"6": map[string]any{"3": map[string]any{"5": string(contentJSON)}},
			"10": map[string]any{
				"reminderContent": reminder,
				"senderUserId":    "buyer-1",
				"sessionType":     "1",
				"reminderUrl":     "https://www.goofish.com/im?itemId=item-1",
			},
		},
	}
}

// TestExtractBuyerImageURLsOnlyReadsDeclaredImageMessages 验证只有平台声明的图片消息才解析图片地址。
func TestExtractBuyerImageURLsOnlyReadsDeclaredImageMessages(t *testing.T) {
	// imageFrame 是正常的买家图片帧，应被解析出全部去重后的地址。
	imageFrame := buyerImageFrame([]string{"https://cdn.example/a.png", "https://cdn.example/b.png", "https://cdn.example/a.png"}, "[图片]")
	// m1 是图片帧的信封层，平台字段 1。
	m1, _ := imageFrame["1"].(map[string]any)
	// m10 是图片帧的展示层，正文与发送者信息都在其中。
	m10, _ := m1["10"].(map[string]any)
	// urls 是解析出的买家图片地址；重复地址必须去重且保持首次出现顺序。
	urls := extractBuyerImageURLs(m1, m10, imageFrame)
	if len(urls) != 2 || urls[0] != "https://cdn.example/a.png" || urls[1] != "https://cdn.example/b.png" {
		t.Fatalf("买家图片地址=%v", urls)
	}
	// textFrame 是普通文本消息，即使内部含有商品图片也不得被当作买家上传。
	textFrame := map[string]any{"1": map[string]any{"10": map[string]any{"reminderContent": "你好", "extJson": `{"contentType":"1"}`}}}
	// got 是文本消息的解析结果；平台只要未声明 contentType=2 就必须为空，不得把商品卡片图片当作买家上传。
	if got := extractBuyerImageURLs(textFrame["1"].(map[string]any), textFrame["1"].(map[string]any)["10"].(map[string]any), textFrame); len(got) != 0 {
		t.Fatalf("文本消息不得解析出买家图片: %v", got)
	}
}

// TestExtractBuyerImageURLsCapsCount 验证单条消息只保留受数量上限约束的图片。
func TestExtractBuyerImageURLsCapsCount(t *testing.T) {
	// manyURLs 超过单条消息允许送入模型的图片数量。
	manyURLs := []string{"https://cdn.example/1.png", "https://cdn.example/2.png", "https://cdn.example/3.png", "https://cdn.example/4.png"}
	// frame 是用上述超量地址构造的买家图片帧。
	frame := buyerImageFrame(manyURLs, "[图片]")
	// m1 是该图片帧的信封层。
	m1, _ := frame["1"].(map[string]any)
	// m10 是该图片帧的展示层。
	m10, _ := m1["10"].(map[string]any)
	// got 是截断后的图片数量，必须恰好等于单条消息上限 buyerImageMaxURLs。
	if got := extractBuyerImageURLs(m1, m10, frame); len(got) != buyerImageMaxURLs {
		t.Fatalf("图片数量=%d, want %d", len(got), buyerImageMaxURLs)
	}
}

// TestExtractChatMessageKeepsBuyerImageMessage 验证纯图片消息不再被“发来一条新消息”摘要丢弃。
func TestExtractChatMessageKeepsBuyerImageMessage(t *testing.T) {
	// frame 是正文摘要为“发来一条新消息”的买家图片帧，用于验证摘要不能覆盖真实图片。
	frame := buyerImageFrame([]string{"https://cdn.example/a.png"}, "发来一条新消息")
	// chat 是解析结果；纯图片消息必须保留并带上占位正文，而不是按系统提示丢弃。
	chat := extractChatMessage(frame, "account-1", "unb=other")
	if chat == nil {
		t.Fatal("买家图片消息不得被丢弃")
	}
	if len(chat.ImageURLs) != 1 || chat.Text != buyerImagePlaceholder {
		t.Fatalf("图片消息=%+v", chat)
	}
	if chat.ItemID != "item-1" {
		t.Fatalf("图片消息商品标识=%q", chat.ItemID)
	}
}

// TestExtractChatMessageStillDropsSystemNotices 验证系统提示与非单聊会话继续被丢弃。
func TestExtractChatMessageStillDropsSystemNotices(t *testing.T) {
	// systemSender 模拟平台系统号推送的图片帧，必须继续丢弃。
	systemSender := buyerImageFrame([]string{"https://cdn.example/a.png"}, "发来一条新消息")
	systemSender["1"].(map[string]any)["10"].(map[string]any)["senderUserId"] = "1400@goofish"
	// chat 是系统号图片帧的解析结果，必须为 nil，避免机器人回复平台自身推送。
	if chat := extractChatMessage(systemSender, "account-1", "unb=other"); chat != nil {
		t.Fatalf("系统号消息不得进入回复链: %+v", chat)
	}
	// groupSession 模拟非单聊会话，同样不得放行。
	groupSession := buyerImageFrame([]string{"https://cdn.example/a.png"}, "发来一条新消息")
	groupSession["1"].(map[string]any)["10"].(map[string]any)["sessionType"] = "2"
	// chat 是非单聊会话图片帧的解析结果，必须为 nil，群聊图片不得触发自动回复。
	if chat := extractChatMessage(groupSession, "account-1", "unb=other"); chat != nil {
		t.Fatalf("非单聊会话不得进入回复链: %+v", chat)
	}
	// noticeFrame 是没有图片的普通系统提示，仍按原规则丢弃。
	noticeFrame := map[string]any{"1": map[string]any{"10": map[string]any{"reminderContent": "发来一条新消息", "senderUserId": "buyer-1", "sessionType": "1"}}}
	// chat 是无图片系统提示的解析结果，必须为 nil，放行规则不得被图片消息放宽。
	if chat := extractChatMessage(noticeFrame, "account-1", "unb=other"); chat != nil {
		t.Fatalf("无图片的系统提示不得进入回复链: %+v", chat)
	}
}

// replaceAIVisionClient 在测试期间替换买家图片下载客户端，并返回恢复函数。
// 该替换只影响当前测试进程，必须在测试结束前调用恢复函数，避免其它用例走测试服务器。
func replaceAIVisionClient(client *http.Client) func() {
	// original 保存替换前的下载客户端构造函数。
	original := newAIVisionHTTPClient
	newAIVisionHTTPClient = func() *http.Client { return client }
	return func() { newAIVisionHTTPClient = original }
}

// TestDownloadAIImageDataURIReturnsEncodedImage 验证成功下载的图片被编码为可直接消费的 data URI。
func TestDownloadAIImageDataURIReturnsEncodedImage(t *testing.T) {
	// payload 是一张最小的 PNG 字节序列，仅用于类型与编码断言。
	payload := append([]byte("\x89PNG\r\n\x1a\n"), []byte("engine-vision-test")...)
	// server 提供带 charset 参数的图片响应，验证类型归一化。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "image/png; charset=binary")
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	// restore 在测试结束后恢复真实下载客户端，避免影响其它用例。
	restore := replaceAIVisionClient(server.Client())
	defer restore()
	// dataURI、err 是编码结果与失败原因。
	dataURI, err := downloadAIImageDataURI(context.Background(), server.URL+"/a.png")
	if err != nil {
		t.Fatalf("下载买家图片失败: %v", err)
	}
	// prefix 是期望的 data URI 前缀。
	prefix := "data:image/png;base64,"
	if !strings.HasPrefix(dataURI, prefix) {
		t.Fatalf("data URI 前缀=%q", dataURI)
	}
	// decoded 是回解出的图片字节，必须与原始内容一致。
	decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURI, prefix))
	if decodeErr != nil || string(decoded) != string(payload) {
		t.Fatalf("图片内容不一致: err=%v len=%d", decodeErr, len(decoded))
	}
}

// TestDownloadAIImageDataURIRejectsUnsupportedResponses 验证非图片、超限与非法地址都被拒绝。
func TestDownloadAIImageDataURIRejectsUnsupportedResponses(t *testing.T) {
	// server 按路径返回不同的失败响应。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/notimage":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = writer.Write([]byte("<html></html>"))
		case "/toobig":
			writer.Header().Set("Content-Type", "image/png")
			_, _ = writer.Write(make([]byte, aiVisionMaxImageBytes+16))
		case "/status":
			writer.WriteHeader(http.StatusForbidden)
		default:
			writer.Header().Set("Content-Type", "image/png")
			_, _ = writer.Write([]byte("\x89PNG\r\n\x1a\nbody"))
		}
	}))
	defer server.Close()
	// restore 在用例结束后恢复真实下载客户端，避免测试服务器地址泄漏到其它用例。
	restore := replaceAIVisionClient(server.Client())
	defer restore()
	// case 是当前待检查的失败场景。
	cases := []struct {
		// name 是该场景的测试名称。
		name string
		// url 是待下载地址。
		url string
	}{
		{name: "non-image", url: server.URL + "/notimage"},
		{name: "too-big", url: server.URL + "/toobig"},
		{name: "http-status", url: server.URL + "/status"},
		{name: "unsupported-scheme", url: "file:///etc/passwd"},
		{name: "no-host", url: "https://"},
		{name: "with-credentials", url: "https://user:pass@example.com/a.png"},
	}
	// testCase 是本轮待验证的失败场景：非图片类型、超出体积上限、非 2xx 状态、非法协议、缺少主机或带凭证地址。
	for _, testCase := range cases {
		// dataURI、err 是该场景的下载结果；地址或内容不可用时必须返回错误，而不是可送入模型的 data URI。
		if dataURI, err := downloadAIImageDataURI(context.Background(), testCase.url); err == nil {
			t.Fatalf("%s 应被拒绝，实际 data URI 前缀=%q", testCase.name, dataURI[:min(len(dataURI), 32)])
		}
	}
}

// TestDownloadAIImageDataURIDetectsMissingContentType 验证服务端未声明类型时按字节嗅探。
func TestDownloadAIImageDataURIDetectsMissingContentType(t *testing.T) {
	// png 是用于嗅探的最小 PNG 头。
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("detect")...)
	// server 故意把类型声明为通用二进制，用于验证按图片字节嗅探真实类型。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(png)
	}))
	defer server.Close()
	// restore 在用例结束后恢复真实下载客户端，避免嗅探用例影响其它用例。
	restore := replaceAIVisionClient(server.Client())
	defer restore()
	// dataURI 必须以嗅探出的 PNG 类型开头。
	dataURI, err := downloadAIImageDataURI(context.Background(), server.URL+"/a.png")
	if err != nil || !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Fatalf("类型嗅探失败: dataURI=%q err=%v", dataURI, err)
	}
}
