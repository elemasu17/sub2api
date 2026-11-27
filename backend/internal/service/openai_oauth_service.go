package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// OpenAIOAuthService handles OpenAI OAuth authentication flows
type OpenAIOAuthService struct {
	sessionStore         *openai.SessionStore
	proxyRepo            ProxyRepository
	oauthClient          OpenAIOAuthClient
	privacyClientFactory PrivacyClientFactory // 用于调用 chatgpt.com/backend-api（ImpersonateChrome）
}

// NewOpenAIOAuthService creates a new OpenAI OAuth service
func NewOpenAIOAuthService(proxyRepo ProxyRepository, oauthClient OpenAIOAuthClient) *OpenAIOAuthService {
	return &OpenAIOAuthService{
		sessionStore: openai.NewSessionStore(),
		proxyRepo:    proxyRepo,
		oauthClient:  oauthClient,
	}
}

// SetPrivacyClientFactory 注入 ImpersonateChrome 客户端工厂，
// 用于调用 chatgpt.com/backend-api 获取账号信息（plan_type 等）。
func (s *OpenAIOAuthService) SetPrivacyClientFactory(factory PrivacyClientFactory) {
	s.privacyClientFactory = factory
}

// OpenAIAuthURLResult contains the authorization URL and session info
type OpenAIAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// GenerateAuthURL generates an OpenAI OAuth authorization URL
func (s *OpenAIOAuthService) GenerateAuthURL(ctx context.Context, proxyID *int64, redirectURI, platform string) (*OpenAIAuthURLResult, error) {
	owner, policyHash, err := openAIOAuthPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateRestrictedOpenAIOAuthInput(ctx, proxyID, redirectURI, platform); err != nil {
		return nil, err
	}
	// Generate PKCE values
	state, err := openai.GenerateState()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_OAUTH_STATE_FAILED", "failed to generate state: %v", err)
	}

	codeVerifier, err := openai.GenerateCodeVerifier()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_OAUTH_VERIFIER_FAILED", "failed to generate code verifier: %v", err)
	}

	codeChallenge := openai.GenerateCodeChallenge(codeVerifier)

	// Generate session ID
	sessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_OAUTH_SESSION_FAILED", "failed to generate session ID: %v", err)
	}

	// Get proxy URL if specified
	var proxyURL string
	if proxyID != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadRequest, "OPENAI_OAUTH_PROXY_NOT_FOUND", "proxy not found: %v", err)
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	// Use default redirect URI if not specified
	if redirectURI == "" {
		redirectURI = openai.DefaultRedirectURI
	}
	normalizedPlatform := normalizeOpenAIOAuthPlatform(platform)
	clientID, _ := openai.OAuthClientConfigByPlatform(normalizedPlatform)

	// Store session
	session := &openai.OAuthSession{
		OwnerKeyID:      owner,
		OwnerPolicyHash: policyHash,
		State:           state,
		CodeVerifier:    codeVerifier,
		ClientID:        clientID,
		RedirectURI:     redirectURI,
		ProxyURL:        proxyURL,
		CreatedAt:       time.Now(),
	}
	s.sessionStore.Set(sessionID, session)

	// Build authorization URL
	authURL := openai.BuildAuthorizationURLForPlatform(state, codeChallenge, redirectURI, normalizedPlatform)

	return &OpenAIAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
		State:     state,
	}, nil
}

// OpenAIExchangeCodeInput represents the input for code exchange
type OpenAIExchangeCodeInput struct {
	AttemptID   string
	SessionID   string
	Code        string
	State       string
	RedirectURI string
	ProxyID     *int64
}

// OpenAITokenInfo represents the token information for OpenAI
type OpenAITokenInfo struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	IDToken               string `json:"id_token,omitempty"`
	ExpiresIn             int64  `json:"expires_in"`
	ExpiresAt             int64  `json:"expires_at"`
	ClientID              string `json:"client_id,omitempty"`
	AuthMode              string `json:"auth_mode,omitempty"`
	Email                 string `json:"email,omitempty"`
	ChatGPTAccountID      string `json:"chatgpt_account_id,omitempty"`
	ChatGPTUserID         string `json:"chatgpt_user_id,omitempty"`
	ChatGPTAccountFedRAMP bool   `json:"chatgpt_account_is_fedramp,omitempty"`
	OrganizationID        string `json:"organization_id,omitempty"`
	PlanType              string `json:"plan_type,omitempty"`
	SubscriptionExpiresAt string `json:"subscription_expires_at,omitempty"`
	PrivacyMode           string `json:"privacy_mode,omitempty"`
}

// ExchangeCode exchanges authorization code for tokens
func (s *OpenAIOAuthService) ExchangeCode(ctx context.Context, input *OpenAIExchangeCodeInput) (*OpenAITokenInfo, error) {
	// Get session
	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	}
	if err := validateOpenAIOAuthSessionOwner(ctx, session, input); err != nil {
		return nil, err
	}
	if input.State == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_STATE_REQUIRED", "oauth state is required")
	}
	if subtle.ConstantTimeCompare([]byte(input.State), []byte(session.State)) != 1 {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_INVALID_STATE", "invalid oauth state")
	}
	if session.OwnerKeyID != "" && !s.sessionStore.Consume(input.SessionID, session) {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	}

	// Get proxy URL: prefer input.ProxyID, fallback to session.ProxyURL
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *input.ProxyID)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadRequest, "OPENAI_OAUTH_PROXY_NOT_FOUND", "proxy not found: %v", err)
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	// Use redirect URI from session or input
	redirectURI := session.RedirectURI
	if input.RedirectURI != "" {
		redirectURI = input.RedirectURI
	}
	clientID := strings.TrimSpace(session.ClientID)
	if clientID == "" {
		clientID = openai.ClientID
	}

	// Exchange code for token
	if input.AttemptID != "" {
		slog.Info("codex_oauth_exchange", "job_id", input.AttemptID, "phase", "upstream_attempt_started")
	}
	tokenResp, err := s.oauthClient.ExchangeCode(ctx, input.Code, session.CodeVerifier, redirectURI, proxyURL, clientID)
	if err != nil {
		if input.AttemptID != "" {
			slog.Info("codex_oauth_exchange", "job_id", input.AttemptID, "phase", "upstream_attempt_failed")
		}
		return nil, err
	}
	if input.AttemptID != "" {
		slog.Info("codex_oauth_exchange", "job_id", input.AttemptID, "phase", "token_received")
	}

	// Delete session after successful exchange
	s.sessionStore.Delete(input.SessionID)
	return s.enrichOAuthResponse(ctx, tokenResp, proxyURL, clientID, nil)
}

// RefreshToken refreshes an OpenAI OAuth token
func (s *OpenAIOAuthService) RefreshToken(ctx context.Context, refreshToken string, proxyURL string) (*OpenAITokenInfo, error) {
	return s.RefreshTokenWithClientID(ctx, refreshToken, proxyURL, "")
}

// RefreshTokenWithClientID refreshes an OpenAI OAuth token with optional client_id.
func (s *OpenAIOAuthService) RefreshTokenWithClientID(ctx context.Context, refreshToken string, proxyURL string, clientID string) (*OpenAITokenInfo, error) {
	return s.refreshTokenForAccount(ctx, refreshToken, proxyURL, clientID, nil)
}

func (s *OpenAIOAuthService) refreshTokenForAccount(ctx context.Context, refreshToken, proxyURL, clientID string, account *Account) (*OpenAITokenInfo, error) {
	tokenResp, err := s.oauthClient.RefreshTokenWithClientID(ctx, refreshToken, proxyURL, clientID)
	if err != nil {
		return nil, err
	}
	return s.enrichOAuthResponse(ctx, tokenResp, proxyURL, clientID, account)
}

func (s *OpenAIOAuthService) enrichOAuthResponse(ctx context.Context, tokenResp *openai.TokenResponse, proxyURL, clientID string, account *Account) (*OpenAITokenInfo, error) {
	tokenInfo := &OpenAITokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresIn:    int64(tokenResp.ExpiresIn),
		ExpiresAt:    time.Now().Unix() + int64(tokenResp.ExpiresIn),
		ClientID:     strings.TrimSpace(clientID),
	}
	if tokenResp.IDToken != "" {
		claims, err := openai.ParseIDToken(tokenResp.IDToken)
		if err != nil {
			slog.Warn("openai_oauth_id_token_parse_failed")
		} else {
			user := claims.GetUserInfo()
			tokenInfo.Email = user.Email
			tokenInfo.ChatGPTAccountID = user.ChatGPTAccountID
			tokenInfo.ChatGPTUserID = user.ChatGPTUserID
			tokenInfo.OrganizationID = user.OrganizationID
			tokenInfo.PlanType = user.PlanType
			if account == nil || validChatGPTSubscriptionExpiry(account.GetCredential("subscription_expires_at")) == "" {
				tokenInfo.SubscriptionExpiresAt = personalSubscriptionSnapshot(claims.OpenAIAuth)
			}
		}
	}
	if err := s.enrichTokenInfo(ctx, tokenInfo, proxyURL, account); err != nil {
		return nil, err
	}
	return tokenInfo, nil
}

// A fresh ID token can still contain a login-time snapshot. Use this only
// when we have no stored expiry and the plan and account are in the same token.
func personalSubscriptionSnapshot(auth *openai.OpenAIAuthClaims) string {
	if auth == nil || strings.TrimSpace(auth.ChatGPTAccountID) == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(auth.ChatGPTPlanType)) {
	case "go", "plus", "pro":
		var expiry string
		if json.Unmarshal(auth.ChatGPTSubscriptionActiveUntil, &expiry) == nil {
			expiry = strings.TrimSpace(expiry)
			if end, err := time.Parse(time.RFC3339, expiry); err == nil && end.After(time.Now()) {
				return expiry
			}
		}
	}
	return ""
}

// enrichTokenInfo 通过 ChatGPT backend-api 补全 tokenInfo 并设置隐私（best-effort）。
// 从 accounts/check 获取最新 plan_type、subscription_expires_at、email，
// 然后尝试关闭训练数据共享。适用于所有获取/刷新 token 的路径。
func (s *OpenAIOAuthService) enrichTokenInfo(ctx context.Context, tokenInfo *OpenAITokenInfo, proxyURL string, account *Account) error {
	orgID := tokenInfo.OrganizationID
	planFromCache := account != nil && account.GetOpenAIRefreshToken() == ""
	var savedID, bearerID, bearerPlan string
	if account != nil {
		savedID = strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	}
	if claims, err := openai.DecodeIDToken(tokenInfo.AccessToken); err == nil && claims.OpenAIAuth != nil {
		auth := claims.OpenAIAuth
		if orgID == "" {
			orgID = auth.POID
		}
		bearerID, bearerPlan = strings.TrimSpace(auth.ChatGPTAccountID), auth.ChatGPTPlanType
	}
	tokenID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	if differentChatGPTAccounts(tokenID, bearerID) || differentChatGPTAccounts(savedID, tokenID) || differentChatGPTAccounts(savedID, bearerID) {
		return infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_ACCOUNT_MISMATCH", "access_denied: OpenAI OAuth account identity mismatch, reauthorization required")
	}
	if tokenID == "" {
		tokenInfo.ChatGPTAccountID = bearerID
		if bearerID == "" {
			tokenInfo.ChatGPTAccountID = savedID
		}
	}
	if tokenInfo.PlanType == "" && bearerID != "" {
		tokenInfo.PlanType = bearerPlan
	}
	if tokenInfo.PlanType == "" && savedID != "" {
		tokenInfo.PlanType = account.GetCredential("plan_type")
		planFromCache = true
	}
	if tokenInfo.AccessToken == "" || s.privacyClientFactory == nil {
		return nil
	}
	subscriptionAccountID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	preferredAccountID := ""
	if strings.TrimSpace(tokenInfo.PlanType) != "" {
		preferredAccountID = subscriptionAccountID
	}
	liveExpiry := ""
	if info := fetchChatGPTAccountInfo(ctx, s.privacyClientFactory, tokenInfo.AccessToken, proxyURL, orgID, preferredAccountID); info != nil {
		// chatgpt_plan_type from the ID token is the canonical personal-plan value.
		// accounts/check is a multi-account/workspace endpoint; inactive team or
		// business workspaces can otherwise overwrite Pro/Free with internal
		// workspace billing plan names such as self_serve_business_usage_based.
		belongs := chatGPTAccountInfoBelongsToTokenAccount(tokenInfo, info)
		appliedAccountInfoPlanType := shouldApplyChatGPTAccountInfoPlanType(tokenInfo.PlanType, info.PlanType) ||
			(planFromCache && belongs && strings.TrimSpace(info.PlanType) != "")
		if appliedAccountInfoPlanType {
			tokenInfo.PlanType = info.PlanType
		}
		// plan_type 与 subscription_expires_at 必须描述同一份订阅。套餐取自
		// accounts/check 时，到期时间跟着取同一条记录；套餐保留了 JWT 里的个人值时，
		// 只有该记录确实就是个人账号才能用它的 entitlement.expires_at——poid 指向的
		// 默认 Personal workspace 与 chatgpt_account_id 可以是两个不同的标识，
		// 混用会显示成「个人 Pro + workspace 到期时间」。
		if appliedAccountInfoPlanType || belongs {
			liveExpiry = info.SubscriptionExpiresAt
		}
		if appliedAccountInfoPlanType && !belongs {
			// A personal billing lookup cannot supply the selected workspace plan's date.
			subscriptionAccountID = ""
		}
		if tokenInfo.Email == "" && info.Email != "" {
			tokenInfo.Email = info.Email
		}
	}
	if liveExpiry == "" && subscriptionAccountID != "" {
		liveExpiry = fetchChatGPTSubscriptionExpiresAt(ctx, s.privacyClientFactory, tokenInfo.AccessToken, proxyURL, subscriptionAccountID)
	}
	if liveExpiry != "" {
		tokenInfo.SubscriptionExpiresAt = liveExpiry
	}

	// 尝试设置隐私（关闭训练数据共享），best-effort
	tokenInfo.PrivacyMode = disableOpenAITraining(ctx, s.privacyClientFactory, tokenInfo.AccessToken, proxyURL)
	return nil
}

func differentChatGPTAccounts(a, b string) bool {
	return a != "" && b != "" && !strings.EqualFold(a, b)
}

func shouldApplyChatGPTAccountInfoPlanType(current, candidate string) bool {
	return strings.TrimSpace(candidate) != "" && strings.TrimSpace(current) == ""
}

// chatGPTAccountInfoBelongsToTokenAccount 判断 accounts/check 命中的那条记录是不是
// token 自己的个人 ChatGPT 账号。两侧都必须有可比较的账号标识。
func chatGPTAccountInfoBelongsToTokenAccount(tokenInfo *OpenAITokenInfo, info *ChatGPTAccountInfo) bool {
	personalID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	sourceID := strings.TrimSpace(info.AccountID)
	return personalID != "" && strings.EqualFold(personalID, sourceID)
}

// RefreshAccountToken refreshes token for an OpenAI OAuth account
func (s *OpenAIOAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*OpenAITokenInfo, error) {
	if account.Platform != PlatformOpenAI {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_INVALID_ACCOUNT", "account is not an OpenAI account")
	}
	if account.Type != AccountTypeOAuth {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_INVALID_ACCOUNT_TYPE", "account is not an OAuth account")
	}

	var proxyURL string
	if account.ProxyID != nil && s.proxyRepo != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
		if err == nil && proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	accessToken := account.GetCredential("access_token")
	if account.IsOpenAIPersonalAccessToken() {
		if accessToken == "" {
			return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_PAT_REQUIRED", "access token is required")
		}
		return s.ValidateCodexPersonalAccessToken(ctx, accessToken, proxyURL)
	}

	refreshToken := account.GetCredential("refresh_token")
	if refreshToken == "" {
		if accessToken != "" {
			tokenInfo := &OpenAITokenInfo{
				AccessToken:      accessToken,
				RefreshToken:     "",
				IDToken:          account.GetCredential("id_token"),
				ClientID:         account.GetCredential("client_id"),
				Email:            account.GetCredential("email"),
				ChatGPTAccountID: account.GetCredential("chatgpt_account_id"),
				ChatGPTUserID:    account.GetCredential("chatgpt_user_id"),
				OrganizationID:   account.GetCredential("organization_id"),
				PlanType:         account.GetCredential("plan_type"),
			}
			if expiresAt := account.GetCredentialAsTime("expires_at"); expiresAt != nil {
				tokenInfo.ExpiresAt = expiresAt.Unix()
				tokenInfo.ExpiresIn = int64(time.Until(*expiresAt).Seconds())
			}
			if err := s.enrichTokenInfo(ctx, tokenInfo, proxyURL, account); err != nil {
				return nil, err
			}
			if tokenInfo.SubscriptionExpiresAt == "" {
				tokenInfo.SubscriptionExpiresAt = validChatGPTSubscriptionExpiry(account.GetCredential("subscription_expires_at"))
			}
			return tokenInfo, nil
		}
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_NO_REFRESH_TOKEN", "no refresh token available")
	}

	clientID := account.GetCredential("client_id")
	return s.refreshTokenForAccount(ctx, refreshToken, proxyURL, clientID, account)
}

// BuildAccountCredentials builds credentials map from token info
func (s *OpenAIOAuthService) BuildAccountCredentials(tokenInfo *OpenAITokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
	}
	if tokenInfo.ExpiresAt > 0 {
		creds["expires_at"] = time.Unix(tokenInfo.ExpiresAt, 0).Format(time.RFC3339)
	}
	// 仅在刷新响应返回了新的 refresh_token 时才更新，防止用空值覆盖已有令牌
	if strings.TrimSpace(tokenInfo.RefreshToken) != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}

	if tokenInfo.IDToken != "" {
		creds["id_token"] = tokenInfo.IDToken
	}
	if tokenInfo.Email != "" {
		creds["email"] = tokenInfo.Email
	}
	if tokenInfo.ChatGPTAccountID != "" {
		creds["chatgpt_account_id"] = tokenInfo.ChatGPTAccountID
	}
	if tokenInfo.ChatGPTUserID != "" {
		creds["chatgpt_user_id"] = tokenInfo.ChatGPTUserID
	}
	if tokenInfo.OrganizationID != "" {
		creds["organization_id"] = tokenInfo.OrganizationID
	}
	if tokenInfo.PlanType != "" {
		creds["plan_type"] = tokenInfo.PlanType
	}
	if expiry := validChatGPTSubscriptionExpiry(tokenInfo.SubscriptionExpiresAt); expiry != "" {
		creds["subscription_expires_at"] = expiry
	}
	if strings.TrimSpace(tokenInfo.ClientID) != "" {
		creds["client_id"] = strings.TrimSpace(tokenInfo.ClientID)
	}
	if tokenInfo.AuthMode == OpenAIAuthModePersonalAccessToken {
		creds[openAIAuthModeCredentialKey] = OpenAIAuthModePersonalAccessToken
		creds[openAIAuthModeLegacyCredentialKey] = "personal_access_token"
		creds["token_type"] = "Bearer"
		creds["chatgpt_account_is_fedramp"] = tokenInfo.ChatGPTAccountFedRAMP
	} else if tokenInfo.ChatGPTAccountFedRAMP {
		creds["chatgpt_account_is_fedramp"] = true
	}

	return NormalizeOpenAIPersonalAccessTokenCredentials(nil, tokenInfo, creds)
}

// Stop stops the session store cleanup goroutine
func (s *OpenAIOAuthService) Stop() {
	s.sessionStore.Stop()
}

func normalizeOpenAIOAuthPlatform(platform string) string {
	return openai.OAuthPlatformOpenAI
}
