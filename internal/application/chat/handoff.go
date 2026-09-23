package chat

import (
	"context"
	"errors"
	"strings"
	"time"
)

// humanHandoffMinMinutes 是手动接管允许的最短时长，单位分钟。
const humanHandoffMinMinutes = 1

// humanHandoffMaxMinutes 是手动接管允许的最长时长，单位分钟；与账号级人工接管配置上限保持一致。
const humanHandoffMaxMinutes = 1440

// ErrHumanHandoffUnavailable 表示当前聊天服务未装配人工接管持久化端口。
var ErrHumanHandoffUnavailable = errors.New("人工接管服务未启用")

// ErrHumanHandoffForbidden 表示当前用户无权操作目标账号的人工接管。
var ErrHumanHandoffForbidden = errors.New("无权操作该账号的人工接管")

// ErrHumanHandoffSessionNotFound 表示目标会话不存在于该账号，无法确定要接管的买家。
var ErrHumanHandoffSessionNotFound = errors.New("聊天会话不存在")

// ErrHumanHandoffInvalidMinutes 表示请求的接管时长超出允许范围。
var ErrHumanHandoffInvalidMinutes = errors.New("人工接管时长必须在 1 到 1440 分钟之间")

// HumanHandoff 是按账号和买家隔离的人工接管状态，不包含任何凭证明文。
type HumanHandoff struct {
	// AccountID 是接管所属账号标识。
	AccountID string
	// BuyerID 是被接管买家的平台标识，始终由服务端从会话推导。
	BuyerID string
	// PausedUntil 是接管截止时间的 Unix 秒；没有接管时为零。
	PausedUntil int64
	// Active 表示请求时刻该买家是否仍处于接管中。
	Active bool
	// RemainingSeconds 是距离接管结束的剩余秒数；未接管时为零。
	RemainingSeconds int
}

// HumanHandoffRepository 定义人工接管用例所需的最小持久化与买家解析能力。
type HumanHandoffRepository interface {
	// ExistsOwned 判断账号是否属于指定用户，只返回存在性而不读取凭证。
	ExistsOwned(ctx context.Context, userID int64, accountID string) (bool, error)
	// FindSessionBuyer 返回会话对端的买家标识；角色未知时可回退为会话对端。
	FindSessionBuyer(ctx context.Context, userID int64, accountID, chatID string) (buyerID string, err error)
	// SetHumanHandoff 覆盖写入接管截止时间（Unix 秒）。
	SetHumanHandoff(ctx context.Context, accountID, buyerID string, pausedUntil int64) error
	// ClearHumanHandoff 删除接管记录；返回是否确实删除了记录。
	ClearHumanHandoff(ctx context.Context, accountID, buyerID string) (bool, error)
	// GetHumanHandoff 读取接管截止时间；没有记录时返回 found=false。
	GetHumanHandoff(ctx context.Context, accountID, buyerID string) (pausedUntil int64, found bool, err error)
}

// humanHandoffNow 提供当前时间；测试可替换为固定时钟以获得确定的剩余时长断言。
var humanHandoffNow = time.Now

// GetHumanHandoff 返回当前用户账号下目标会话买家的接管状态。
func (s *Service) GetHumanHandoff(ctx context.Context, userID int64, accountID, chatID string) (HumanHandoff, error) {
	// repository、buyerID 和 err 保存装配校验后的仓储端口、买家和解析错误。
	repository, buyerID, err := s.resolveHumanHandoffTarget(ctx, userID, accountID, chatID)
	if err != nil {
		return HumanHandoff{}, err
	}
	return readHumanHandoff(ctx, repository, accountID, buyerID)
}

// SetHumanHandoff 按操作者选择的分钟数设置接管，并返回设置后的接管状态。
func (s *Service) SetHumanHandoff(ctx context.Context, userID int64, accountID, chatID string, minutes int) (HumanHandoff, error) {
	if minutes < humanHandoffMinMinutes || minutes > humanHandoffMaxMinutes {
		return HumanHandoff{}, ErrHumanHandoffInvalidMinutes
	}
	// repository、buyerID 和 err 保存装配校验后的仓储端口、买家和解析错误。
	repository, buyerID, err := s.resolveHumanHandoffTarget(ctx, userID, accountID, chatID)
	if err != nil {
		return HumanHandoff{}, err
	}
	// pausedUntil 是按当前时间与所选分钟数换算出的接管截止 Unix 秒。
	pausedUntil := humanHandoffNow().UTC().Add(time.Duration(minutes) * time.Minute).Unix()
	// err 是写入接管记录失败的原因；写入失败时直接向上返回，不读回接管状态。
	if err := repository.SetHumanHandoff(ctx, accountID, buyerID, pausedUntil); err != nil {
		return HumanHandoff{}, err
	}
	return readHumanHandoff(ctx, repository, accountID, buyerID)
}

// ClearHumanHandoff 提前结束接管，使 AI 可以立即恢复回复该买家。
func (s *Service) ClearHumanHandoff(ctx context.Context, userID int64, accountID, chatID string) (HumanHandoff, error) {
	// repository、buyerID 和 err 保存装配校验后的仓储端口、买家和解析错误。
	repository, buyerID, err := s.resolveHumanHandoffTarget(ctx, userID, accountID, chatID)
	if err != nil {
		return HumanHandoff{}, err
	}
	// err 保存删除接管记录的错误；目标不存在时按幂等成功处理。
	if _, err := repository.ClearHumanHandoff(ctx, accountID, buyerID); err != nil {
		return HumanHandoff{}, err
	}
	return HumanHandoff{AccountID: accountID, BuyerID: buyerID}, nil
}

// readHumanHandoff 读取接管记录并换算剩余时长；记录已过期时按未接管返回。
func readHumanHandoff(ctx context.Context, repository HumanHandoffRepository, accountID, buyerID string) (HumanHandoff, error) {
	// pausedUntil、found 和 err 保存接管截止时间、存在状态与读取错误。
	pausedUntil, found, err := repository.GetHumanHandoff(ctx, accountID, buyerID)
	if err != nil {
		return HumanHandoff{}, err
	}
	// result 是本次读取的对外结果；未记录或已过期时保持零值并只回填隔离键。
	result := HumanHandoff{AccountID: accountID, BuyerID: buyerID}
	if !found {
		return result, nil
	}
	// remaining 是距离截止时间的剩余秒数；非正数表示接管已经结束。
	remaining := pausedUntil - humanHandoffNow().UTC().Unix()
	if remaining <= 0 {
		return result, nil
	}
	result.PausedUntil = pausedUntil
	result.Active = true
	result.RemainingSeconds = int(remaining)
	return result, nil
}

// resolveHumanHandoffTarget 校验装配、账号归属和会话存在性，并返回买家标识。
// 买家始终由服务端从会话推导，避免调用方直接指定买家而误操作其他人的会话。
func (s *Service) resolveHumanHandoffTarget(ctx context.Context, userID int64, accountID, chatID string) (HumanHandoffRepository, string, error) {
	// normalizedAccountID、normalizedChatID 保存去除空白后的隔离键。
	normalizedAccountID, normalizedChatID := strings.TrimSpace(accountID), strings.TrimSpace(chatID)
	if s == nil || s.repository == nil || userID <= 0 || normalizedAccountID == "" || normalizedChatID == "" {
		return nil, "", ErrInvalidInput
	}
	// repository 保存从聊天基础仓储断言得到的人工接管能力。
	repository, ok := s.repository.(HumanHandoffRepository)
	if !ok {
		return nil, "", ErrHumanHandoffUnavailable
	}
	// owned 和 ownershipErr 保存非敏感账号所有权判定及查询错误。
	owned, ownershipErr := repository.ExistsOwned(ctx, userID, normalizedAccountID)
	if ownershipErr != nil {
		return nil, "", ownershipErr
	}
	if !owned {
		return nil, "", ErrHumanHandoffForbidden
	}
	// buyerID、buyerErr 保存从会话推导出的买家标识与解析错误。
	buyerID, buyerErr := repository.FindSessionBuyer(ctx, userID, normalizedAccountID, normalizedChatID)
	if buyerErr != nil {
		return nil, "", buyerErr
	}
	if strings.TrimSpace(buyerID) == "" {
		return nil, "", ErrHumanHandoffSessionNotFound
	}
	return repository, strings.TrimSpace(buyerID), nil
}
