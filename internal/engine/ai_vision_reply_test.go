package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"xianyu-go/internal/db"
)

// visionContentPart 是多模态正文中的单个分区，图片分区的 URL 为内联 data URI。
type visionContentPart struct {
	// Type 是分区类型，文本为 text、图片为 image_url。
	Type string `json:"type"`
	// Text 是文本分区内容；图片分区为空。
	Text string `json:"text"`
	// ImageURL 是图片分区内容。
	ImageURL *struct {
		// URL 是图片地址或内联 data URI。
		URL string `json:"url"`
	} `json:"image_url"`
}

// visionModelRequest 是模型请求体中用于多模态断言的最小结构。
type visionModelRequest struct {
	// Messages 是按顺序发送的消息列表；content 可能是字符串或多模态数组，必须按原始 JSON 分流解析。
	Messages []struct {
		// Role 是消息角色。
		Role string `json:"role"`
		// Content 是未经类型解释的正文，用于兼容纯文本与多模态两种编码。
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// lastUserMessage 解析模型请求中最后一条用户消息，返回纯文本正文与多模态分区。
// 纯文本消息返回 text 且 parts 为空；多模态消息返回空 text 与分区列表。
func lastUserMessage(t *testing.T, request visionModelRequest) (string, []visionContentPart) {
	t.Helper()
	if len(request.Messages) == 0 {
		t.Fatal("模型请求没有消息")
	}
	// raw 是最后一条消息的正文原始 JSON。
	raw := request.Messages[len(request.Messages)-1].Content
	if len(raw) == 0 {
		return "", nil
	}
	// text 先按纯文本正文尝试解析；成功即表示本次为纯文本请求。
	var text string
	// err 是纯文本解析结果；为 nil 表示正文是 JSON 字符串，本次请求没有多模态分区。
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	// parts 是数组形式的多模态正文。
	var parts []visionContentPart
	// err 是数组解析失败原因；两种编码都不匹配说明断言夹具与实现脱节，直接终止用例而不是误判。
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("解析模型消息正文失败: %v raw=%s", err, strings.TrimSpace(string(raw)))
	}
	return "", parts
}

// setupVisionModelServer 启动一个返回固定回复并记录请求体的模型服务。
// replies 按调用次序提供回复文本；firstFails 为真时首个请求返回错误，用于验证纯文本降级。
func setupVisionModelServer(t *testing.T, replies []string, firstFails *atomic.Bool) (*httptest.Server, func() visionModelRequest) {
	t.Helper()
	// calls 记录已经处理的模型请求次数，用于区分首轮失败与重试。
	var calls int32
	// latest 保存最近一次请求体，供断言读取。
	var latest visionModelRequest
	// server 是记录请求体并返回固定回复的假模型服务，避免用例访问真实模型接口。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// decoded 是本次请求体；解析失败只记录错误并继续返回合法响应，避免被测客户端因等待而挂起。
		var decoded visionModelRequest
		// err 是请求体解码失败原因；出现即说明请求格式不符合预期，但用例仍需走完回复流程。
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("解析模型请求失败: %v", err)
		}
		latest = decoded
		// index 是本次调用的序号；超出回复数量时复用最后一条。
		index := int(atomic.AddInt32(&calls, 1)) - 1
		if firstFails != nil && firstFails.Load() && index == 0 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"this model does not support image input"}}`))
			return
		}
		if index >= len(replies) {
			index = len(replies) - 1
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + replies[index] + `"}}]}`))
	}))
	t.Cleanup(server.Close)
	return server, func() visionModelRequest { return latest }
}

// setupVisionAccount 为账号写入指定图片识别开关的完全模式配置，并指向测试模型服务。
func setupVisionAccount(t *testing.T, store *db.Store, modelURL string, visionEnabled bool) {
	t.Helper()
	// ctx 是测试夹具写入使用的上下文。
	ctx := context.Background()
	// err 是账号 AI 配置写入失败原因；配置未落库会让后续回复读到旧值，必须立即终止用例。
	if err := store.AIReply.UpsertSettings(ctx, "cid", db.AIReplySettings{
		AIEnabled: true, ReplyMode: db.AIReplyModeFull, VisionEnabled: visionEnabled, MaxBargainRounds: 3,
	}); err != nil {
		t.Fatal(err)
	}
	// err 是模型密钥写入失败原因；这里使用本地假服务密钥，不涉及真实凭证。
	if err := store.Settings.Set(ctx, "ai_api_key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	// err 是模型地址写入失败原因；地址必须指向测试服务，否则用例会真实出网。
	if err := store.Settings.Set(ctx, "ai_api_url", modelURL); err != nil {
		t.Fatal(err)
	}
}

// visionImageMessage 返回一条带图片地址的买家消息。
func visionImageMessage() ChatMessage {
	// message 是带图片地址的买家消息；图片地址由测试图片服务提供。
	message := chatMsg(buyerImagePlaceholder, "item-vision", "chat-vision")
	message.SenderUserID = "buyer-vision"
	return message
}

// newVisionImageServer 启动返回最小 PNG 字节的图片服务，并替换下载客户端。
// 返回图片服务地址与恢复函数；restore 必须在测试结束前调用。
func newVisionImageServer(t *testing.T, hits *int32) (string, func()) {
	t.Helper()
	// server 是只返回最小 PNG 字节的图片服务，用于替代真实买家图片 CDN。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(append([]byte("\x89PNG\r\n\x1a\n"), []byte("vision")...))
	}))
	t.Cleanup(server.Close)
	return server.URL, replaceAIVisionClient(server.Client())
}

// TestAIReplySendsBuyerImageAsMultimodalContent 验证启用识别时买家图片以 image_url 分区随消息发送。
func TestAIReplySendsBuyerImageAsMultimodalContent(t *testing.T) {
	// store、cleanup 提供隔离的账号与设置数据。
	store, cleanup := newAIStore(t)
	defer cleanup()
	// imageURL、imageRestore 提供可下载图片并恢复下载客户端。
	imageURL, imageRestore := newVisionImageServer(t, nil)
	defer imageRestore()
	// modelServer、latestRequest 提供模型服务并暴露最近请求体。
	modelServer, latestRequest := setupVisionModelServer(t, []string{"我看到图片了"}, nil)
	setupVisionAccount(t, store, modelServer.URL, true)
	// message 是本次买家图片消息。
	message := visionImageMessage()
	message.ImageURLs = []string{imageURL + "/a.png"}
	// result、err 是回复结果与失败原因。
	result, err := NewAIReplier("cid", store, nil).Reply(context.Background(), message)
	if err != nil || result == nil || result.Text != "我看到图片了" {
		t.Fatalf("图片消息回复失败: result=%+v err=%v", result, err)
	}
	// text、parts 是模型收到的最后一条用户消息正文。
	text, parts := lastUserMessage(t, latestRequest())
	if len(parts) != 2 {
		t.Fatalf("多模态分区数量=%d text=%q", len(parts), text)
	}
	if parts[0].Type != "text" || parts[0].Text != buyerImagePlaceholder {
		t.Fatalf("文本分区=%+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("图片分区=%+v", parts[1])
	}
	if text != "" {
		t.Fatalf("多模态消息不得同时携带纯文本 content: %q", text)
	}
}

// TestAIReplySkipsImageWhenVisionDisabled 验证关闭开关后图片不会进入模型请求。
func TestAIReplySkipsImageWhenVisionDisabled(t *testing.T) {
	// store、cleanup 提供隔离的账号与设置数据。
	store, cleanup := newAIStore(t)
	defer cleanup()
	// imageHits 统计图片服务命中次数；关闭开关时本不应被访问。
	var imageHits int32
	// imageURL、imageRestore 是图片服务地址与其下载客户端恢复函数；关闭开关时该地址不应被访问。
	imageURL, imageRestore := newVisionImageServer(t, &imageHits)
	defer imageRestore()
	// modelServer、latestRequest 是假模型服务与其最近请求体，用于确认发送内容不含图片分区。
	modelServer, latestRequest := setupVisionModelServer(t, []string{"纯文字回复"}, nil)
	setupVisionAccount(t, store, modelServer.URL, false)
	// message 是带图片地址的买家消息；图片识别关闭时该地址必须被忽略。
	message := visionImageMessage()
	message.ImageURLs = []string{imageURL + "/a.png"}
	// err 是回复失败原因；关闭图片识别只影响是否带图，纯文本回复本身仍必须成功。
	if _, err := NewAIReplier("cid", store, nil).Reply(context.Background(), message); err != nil {
		t.Fatalf("关闭图片识别时回复失败: %v", err)
	}
	// got 是图片服务的实际命中次数，开关关闭后必须为 0，证明没有多余的外部下载。
	if got := atomic.LoadInt32(&imageHits); got != 0 {
		t.Fatalf("关闭图片识别后仍下载了图片: %d 次", got)
	}
	// text、parts 必须是纯文本正文。
	text, parts := lastUserMessage(t, latestRequest())
	if len(parts) != 0 || text != buyerImagePlaceholder {
		t.Fatalf("关闭开关后应发送纯文本: text=%q parts=%+v", text, parts)
	}
}

// TestAIReplyFallsBackToTextWhenModelRejectsImages 验证模型不支持视觉时退回纯文本重试。
func TestAIReplyFallsBackToTextWhenModelRejectsImages(t *testing.T) {
	// store、cleanup 提供隔离的账号与设置数据。
	store, cleanup := newAIStore(t)
	defer cleanup()
	// imageURL、imageRestore 是可用图片服务地址与其下载客户端恢复函数；首轮请求会真的带上该图片。
	imageURL, imageRestore := newVisionImageServer(t, nil)
	defer imageRestore()
	// firstFails 让首轮带图请求失败，模拟不支持视觉输入的模型。
	var firstFails atomic.Bool
	firstFails.Store(true)
	// modelServer、latestRequest 是假模型服务与其最近请求体，用于确认重试请求已去掉图片分区。
	modelServer, latestRequest := setupVisionModelServer(t, []string{"降级回复"}, &firstFails)
	setupVisionAccount(t, store, modelServer.URL, true)
	// message 是带可用图片地址的买家消息，首轮会以多模态形式发出。
	message := visionImageMessage()
	message.ImageURLs = []string{imageURL + "/a.png"}
	// result、err 是降级后的回复结果与失败原因。
	result, err := NewAIReplier("cid", store, nil).Reply(context.Background(), message)
	if err != nil || result == nil || result.Text != "降级回复" {
		t.Fatalf("带图失败后应退回纯文本回复: result=%+v err=%v", result, err)
	}
	// text、parts 是重试请求的用户消息，必须是纯文本。
	text, parts := lastUserMessage(t, latestRequest())
	if len(parts) != 0 || text != buyerImagePlaceholder {
		t.Fatalf("重试请求应为纯文本: text=%q parts=%+v", text, parts)
	}
}

// TestAIReplyFallsBackToTextWhenAllImagesUnavailable 验证图片全部下载失败时仍按纯文本回复。
func TestAIReplyFallsBackToTextWhenAllImagesUnavailable(t *testing.T) {
	// store、cleanup 提供隔离的账号与设置数据。
	store, cleanup := newAIStore(t)
	defer cleanup()
	// imageServer 对所有图片请求返回非图片内容，模拟买家图片已失效。
	imageServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("<html>expired</html>"))
	}))
	defer imageServer.Close()
	// imageRestore 在用例结束后恢复真实下载客户端，避免失效图片服务影响其它用例。
	imageRestore := replaceAIVisionClient(imageServer.Client())
	defer imageRestore()
	// modelServer、latestRequest 是假模型服务与其最近请求体，用于确认全部图片不可用时仍退回纯文本。
	modelServer, latestRequest := setupVisionModelServer(t, []string{"仍然回复"}, nil)
	setupVisionAccount(t, store, modelServer.URL, true)
	// message 是图片已失效的买家消息，正文占位仍应触发正常回复。
	message := visionImageMessage()
	message.ImageURLs = []string{imageServer.URL + "/gone.png"}
	// result、err 是纯文本降级后的结果与失败原因。
	result, err := NewAIReplier("cid", store, nil).Reply(context.Background(), message)
	if err != nil || result == nil || result.Text != "仍然回复" {
		t.Fatalf("图片不可用时仍应回复: result=%+v err=%v", result, err)
	}
	// parts 是模型收到的最后一条用户消息分区；图片全部不可用时必须为空，表示已退回纯文本。
	if _, parts := lastUserMessage(t, latestRequest()); len(parts) != 0 {
		t.Fatalf("无可用图片时应发送纯文本: parts=%+v", parts)
	}
}
