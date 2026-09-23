package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	itemapp "xianyu-go/internal/application/items"
	"xianyu-go/internal/auth"
)

// itemAIPromptRequest 是商品级 AI 提示词保存请求 DTO。
type itemAIPromptRequest struct {
	// Strategy 是商品提示词合并策略。
	Strategy string `json:"strategy"`
	// Prompt 是商品级提示词正文。
	Prompt string `json:"prompt"`
	// CustomVariables 是商品级自定义变量。
	CustomVariables map[string]string `json:"custom_variables"`
}

// itemAIPromptResponse 是商品级 AI 提示词响应 DTO。
type itemAIPromptResponse struct {
	// CookieID 是配置所属账号标识。
	CookieID string `json:"cookie_id"`
	// ItemID 是平台商品标识。
	ItemID string `json:"item_id"`
	// Strategy 是商品提示词合并策略。
	Strategy string `json:"strategy"`
	// Prompt 是商品级提示词正文。
	Prompt string `json:"prompt"`
	// CustomVariables 是商品级自定义变量。
	CustomVariables map[string]string `json:"custom_variables"`
}

// getItemAIPrompt 读取当前用户商品的 AI 提示词配置。
func (s *Server) getItemAIPrompt(w http.ResponseWriter, r *http.Request) {
	// cookieID、itemID 是路径中的账号与商品标识，用于归属校验和商品定位。
	cookieID, itemID := chi.URLParam(r, "cookie_id"), chi.URLParam(r, "item_id")
	// session 是当前请求的登录会话；未登录时为零值，应用层会按无权访问拒绝。
	session := auth.SessionFromContext(r.Context())
	// userID 是会话所属用户主键，用于校验账号归属；仅用于授权判断，不进入响应体。
	userID := session.UserID
	// prompt 是读取到的商品级提示词配置，err 是应用层返回的归属、存在性或存储错误。
	prompt, err := s.itemAIPromptApplication().GetAIPrompt(r.Context(), userID, cookieID, itemID)
	if err != nil {
		writeItemAIPromptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, itemAIPromptResponse{CookieID: prompt.CookieID, ItemID: prompt.ItemID, Strategy: prompt.Strategy, Prompt: prompt.Prompt, CustomVariables: prompt.CustomVariables})
}

// putItemAIPrompt 保存当前用户商品的 AI 提示词配置。
func (s *Server) putItemAIPrompt(w http.ResponseWriter, r *http.Request) {
	// cookieID、itemID 是路径中的账号与商品标识，用于归属校验和商品定位。
	cookieID, itemID := chi.URLParam(r, "cookie_id"), chi.URLParam(r, "item_id")
	// session 是当前请求的登录会话；未登录时为零值，应用层会按无权访问拒绝。
	session := auth.SessionFromContext(r.Context())
	// userID 是会话所属用户主键，保存配置前必须据此校验账号归属。
	userID := session.UserID
	// request 是保存请求 DTO；自定义变量与策略在此从 JSON 请求体反序列化。
	var request itemAIPromptRequest
	// err 是请求体解析失败的错误，此时按 400 拒绝且不触碰存储。
	if err := decodeJSON(r, &request); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	// prompt 是保存后回读的配置（已归一化账号与商品标识），err 是应用层的校验、归属或存储错误。
	prompt, err := s.itemAIPromptApplication().SaveAIPrompt(r.Context(), userID, itemapp.ItemAIPrompt{CookieID: cookieID, ItemID: itemID, Strategy: request.Strategy, Prompt: request.Prompt, CustomVariables: request.CustomVariables})
	if err != nil {
		writeItemAIPromptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, itemAIPromptResponse{CookieID: prompt.CookieID, ItemID: prompt.ItemID, Strategy: prompt.Strategy, Prompt: prompt.Prompt, CustomVariables: prompt.CustomVariables})
}

// writeItemAIPromptError 将商品级 AI 提示词应用错误映射为统一 HTTP 错误。
func writeItemAIPromptError(w http.ResponseWriter, err error) {
	// status 是默认的内部错误状态码，命中已知业务错误后会被覆盖为 400/403/404。
	status := http.StatusInternalServerError
	// message 是默认的通用失败文案，命中已知业务错误后替换为对外可见的具体说明且不包含内部细节。
	message := "商品 AI 提示词操作失败"
	if errors.Is(err, itemapp.ErrAIPromptInvalid) {
		status, message = http.StatusBadRequest, err.Error()
	} else if errors.Is(err, itemapp.ErrAIPromptForbidden) {
		status, message = http.StatusForbidden, "无权访问该账号"
	} else if errors.Is(err, itemapp.ErrAIPromptNotFound) {
		status, message = http.StatusNotFound, "商品不存在"
	}
	writeErr(w, status, message)
}
