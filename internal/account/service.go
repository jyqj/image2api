package account

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/432539/gpt2api/pkg/crypto"
)

// Service 账号池业务。
type Service struct {
	dao    *DAO
	cipher *crypto.AESGCM
}

func NewService(dao *DAO, cipher *crypto.AESGCM) *Service {
	return &Service{dao: dao, cipher: cipher}
}

// CreateInput 新增账号入参(明文敏感字段)。
type CreateInput struct {
	Email            string    `json:"email"`
	AuthToken        string    `json:"auth_token"`
	RefreshToken     string    `json:"refresh_token"`
	SessionToken     string    `json:"session_token"`
	TokenExpiresAt   time.Time `json:"token_expires_at"`
	OAISessionID     string    `json:"oai_session_id"`
	OAIDeviceID      string    `json:"oai_device_id"`
	ClientID         string    `json:"client_id"`
	ChatGPTAccountID string    `json:"chatgpt_account_id"`
	AccountType      string    `json:"account_type"`
	SubscriptionType string    `json:"subscription_type"`
	Notes            string    `json:"notes"`
	Cookies          string    `json:"cookies"`
	ProxyID          uint64    `json:"proxy_id"` // 可选:立即绑定
}

// UpdateInput 更新入参。AuthToken/RefreshToken/SessionToken/Cookies 为空串表示不改。
type UpdateInput struct {
	Email            string    `json:"email"`
	AuthToken        string    `json:"auth_token"`
	RefreshToken     string    `json:"refresh_token"`
	SessionToken     string    `json:"session_token"`
	TokenExpiresAt   time.Time `json:"token_expires_at"`
	OAISessionID     string    `json:"oai_session_id"`
	OAIDeviceID      string    `json:"oai_device_id"`
	ClientID         string    `json:"client_id"`
	ChatGPTAccountID string    `json:"chatgpt_account_id"`
	AccountType      string    `json:"account_type"`
	SubscriptionType string    `json:"subscription_type"`
	Status           string    `json:"status"`
	Notes            string    `json:"notes"`
	Cookies          string    `json:"cookies"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Account, error) {
	if in.Email == "" || in.AuthToken == "" {
		return nil, errors.New("email 和 auth_token 不能为空")
	}
	atEnc, err := s.cipher.EncryptString(in.AuthToken)
	if err != nil {
		return nil, err
	}
	var rtEnc, stEnc sql.NullString
	if in.RefreshToken != "" {
		v, err := s.cipher.EncryptString(in.RefreshToken)
		if err != nil {
			return nil, err
		}
		rtEnc = sql.NullString{String: v, Valid: true}
	}
	if in.SessionToken != "" {
		v, err := s.cipher.EncryptString(in.SessionToken)
		if err != nil {
			return nil, err
		}
		stEnc = sql.NullString{String: v, Valid: true}
	}
	if in.OAIDeviceID == "" {
		in.OAIDeviceID = uuid.NewString()
	}
	if in.OAISessionID == "" {
		in.OAISessionID = uuid.NewString()
	}
	if in.SubscriptionType == "" {
		// 尝试从 AT 的 JWT 自动识别 plan_type
		if pt := parseJWTPlanType(in.AuthToken); pt != "" {
			in.SubscriptionType = pt
		} else {
			in.SubscriptionType = "plus"
		}
	}
	if in.ClientID == "" {
		in.ClientID = "app_LlGpXReQgckcGGUo2JrYvtJK"
	}
	if in.AccountType == "" {
		in.AccountType = "codex"
	}
	a := &Account{
		Email: in.Email, AuthTokenEnc: atEnc, RefreshTokenEnc: rtEnc, SessionTokenEnc: stEnc,
		OAISessionID: in.OAISessionID, OAIDeviceID: in.OAIDeviceID,
		ClientID: in.ClientID, ChatGPTAccountID: in.ChatGPTAccountID, AccountType: in.AccountType,
		SubscriptionType: in.SubscriptionType,
		Status: StatusHealthy, Notes: in.Notes,
	}
	if !in.TokenExpiresAt.IsZero() {
		a.TokenExpiresAt = sql.NullTime{Time: in.TokenExpiresAt, Valid: true}
	} else {
		// 自动从 JWT 解析 exp
		if exp := parseJWTExp(in.AuthToken); !exp.IsZero() {
			a.TokenExpiresAt = sql.NullTime{Time: exp, Valid: true}
		}
	}
	id, err := s.dao.Create(ctx, a)
	if err != nil {
		return nil, err
	}
	a.ID = id
	if in.Cookies != "" {
		enc, err := s.cipher.EncryptString(in.Cookies)
		if err != nil {
			return nil, err
		}
		if err := s.dao.UpsertCookies(ctx, id, enc); err != nil {
			return nil, err
		}
	}
	// 绑定代理:指定了就用指定的,否则自动分配绑定数最少的
	proxyID := in.ProxyID
	if proxyID == 0 {
		if autoID, err := s.dao.LeastBoundProxyID(ctx, nil); err == nil {
			proxyID = autoID
		}
	}
	if proxyID > 0 {
		if err := s.dao.SetBinding(ctx, id, proxyID); err != nil {
			return nil, err
		}
	}
	return s.dao.GetByID(ctx, id)
}

func (s *Service) Update(ctx context.Context, id uint64, in UpdateInput) (*Account, error) {
	a, err := s.dao.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Email != "" {
		a.Email = in.Email
	}
	if in.AuthToken != "" {
		enc, err := s.cipher.EncryptString(in.AuthToken)
		if err != nil {
			return nil, err
		}
		a.AuthTokenEnc = enc
	}
	if in.RefreshToken != "" {
		enc, err := s.cipher.EncryptString(in.RefreshToken)
		if err != nil {
			return nil, err
		}
		a.RefreshTokenEnc = sql.NullString{String: enc, Valid: true}
	}
	if in.SessionToken != "" {
		enc, err := s.cipher.EncryptString(in.SessionToken)
		if err != nil {
			return nil, err
		}
		a.SessionTokenEnc = sql.NullString{String: enc, Valid: true}
	}
	if !in.TokenExpiresAt.IsZero() {
		a.TokenExpiresAt = sql.NullTime{Time: in.TokenExpiresAt, Valid: true}
	} else if in.AuthToken != "" {
		if exp := parseJWTExp(in.AuthToken); !exp.IsZero() {
			a.TokenExpiresAt = sql.NullTime{Time: exp, Valid: true}
		}
	}
	if in.OAISessionID != "" {
		a.OAISessionID = in.OAISessionID
	}
	if in.OAIDeviceID != "" {
		a.OAIDeviceID = in.OAIDeviceID
	}
	if in.ClientID != "" {
		a.ClientID = in.ClientID
	}
	if in.ChatGPTAccountID != "" {
		a.ChatGPTAccountID = in.ChatGPTAccountID
	}
	if in.AccountType != "" {
		a.AccountType = in.AccountType
	}
	if in.SubscriptionType != "" {
		a.SubscriptionType = in.SubscriptionType
	}
	if in.Status != "" {
		a.Status = in.Status
	}
	a.Notes = in.Notes
	if err := s.dao.Update(ctx, a); err != nil {
		return nil, err
	}
	if in.Cookies != "" {
		enc, err := s.cipher.EncryptString(in.Cookies)
		if err != nil {
			return nil, err
		}
		if err := s.dao.UpsertCookies(ctx, id, enc); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (s *Service) Delete(ctx context.Context, id uint64) error {
	return s.dao.SoftDelete(ctx, id)
}

// BulkDeleteByStatus 批量软删;status 支持 dead / suspicious / warned / throttled / all。
func (s *Service) BulkDeleteByStatus(ctx context.Context, status string) (int64, error) {
	if status == "all" {
		return s.dao.SoftDeleteByStatus(ctx, "")
	}
	return s.dao.SoftDeleteByStatus(ctx, status)
}

func (s *Service) Get(ctx context.Context, id uint64) (*Account, error) {
	return s.dao.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, status, keyword string, offset, limit int) ([]*Account, int64, error) {
	return s.dao.List(ctx, status, keyword, offset, limit)
}

// BindProxy 绑定代理(一号一代理)。
func (s *Service) BindProxy(ctx context.Context, accountID, proxyID uint64) error {
	return s.dao.SetBinding(ctx, accountID, proxyID)
}

// UnbindProxy 解除绑定。
func (s *Service) UnbindProxy(ctx context.Context, accountID uint64) error {
	return s.dao.RemoveBinding(ctx, accountID)
}

// DecryptAuthToken 解密 AT。
func (s *Service) DecryptAuthToken(a *Account) (string, error) {
	return s.cipher.DecryptString(a.AuthTokenEnc)
}

// AccountSecrets AT / RT / ST 明文,仅给本地控制台编辑页回填使用。
type AccountSecrets struct {
	AuthToken    string `json:"auth_token"`
	RefreshToken string `json:"refresh_token"`
	SessionToken string `json:"session_token"`
}

// GetSecrets 返回指定账号的 AT/RT/ST 明文(用于后台编辑弹窗回显)。
func (s *Service) GetSecrets(ctx context.Context, id uint64) (*AccountSecrets, error) {
	a, err := s.dao.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &AccountSecrets{}
	if a.AuthTokenEnc != "" {
		if v, err := s.cipher.DecryptString(a.AuthTokenEnc); err == nil {
			out.AuthToken = v
		}
	}
	if a.RefreshTokenEnc.Valid && a.RefreshTokenEnc.String != "" {
		if v, err := s.cipher.DecryptString(a.RefreshTokenEnc.String); err == nil {
			out.RefreshToken = v
		}
	}
	if a.SessionTokenEnc.Valid && a.SessionTokenEnc.String != "" {
		if v, err := s.cipher.DecryptString(a.SessionTokenEnc.String); err == nil {
			out.SessionToken = v
		}
	}
	return out, nil
}

// DecryptCookies 返回账号 cookies 明文(JSON 字符串)。
func (s *Service) DecryptCookies(ctx context.Context, accountID uint64) (string, error) {
	enc, err := s.dao.GetCookies(ctx, accountID)
	if err != nil {
		return "", err
	}
	if enc == "" {
		return "", nil
	}
	return s.cipher.DecryptString(enc)
}

// GetBinding 查账号-代理绑定。
func (s *Service) GetBinding(ctx context.Context, accountID uint64) (*Binding, error) {
	return s.dao.GetBinding(ctx, accountID)
}

// DAO 暴露给调度器使用。
func (s *Service) DAO() *DAO { return s.dao }
