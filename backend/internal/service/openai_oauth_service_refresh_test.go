package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type subscriptionOAuthClient struct {
	openaiOAuthClientRefreshStub
	response openai.TokenResponse
}

func (c *subscriptionOAuthClient) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	return &c.response, nil
}

func (c *subscriptionOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	return &c.response, nil
}

func subscriptionJWT(t *testing.T, auth map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"https://api.openai.com/auth": auth})
	require.NoError(t, err)
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture"
}

func TestOpenAISubscriptionExpiryEnrichmentPaths(t *testing.T) {
	const live = "2027-12-01T00:00:00Z"
	for _, path := range []string{"exchange", "refresh", "access_only"} {
		t.Run(path, func(t *testing.T) {
			// No id_token. poid is not a ChatGPT billing account ID.
			at := subscriptionJWT(t, map[string]any{"chatgpt_account_id": "billing", "poid": "org", "chatgpt_plan_type": "plus"})
			client := &subscriptionOAuthClient{response: openai.TokenResponse{AccessToken: at}}
			svc := NewOpenAIOAuthService(nil, client)
			t.Cleanup(svc.Stop)
			server := newChatGPTBackendTestServer(t, chatGPTBackendTestServerConfig{
				onSubscription: func(accountID string) map[string]any {
					require.Equal(t, "billing", accountID)
					return map[string]any{"active_until": live}
				},
			})
			t.Cleanup(server.Close)
			svc.SetPrivacyClientFactory(newTestPrivacyClientFactory(server))
			var info *OpenAITokenInfo
			var err error
			switch path {
			case "exchange":
				svc.sessionStore.Set("sid", &openai.OAuthSession{State: "state", CreatedAt: time.Now()})
				info, err = svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: "sid", Code: "code", State: "state"})
			case "refresh":
				info, err = svc.RefreshTokenWithClientID(context.Background(), "rt", "", "")
			case "access_only":
				info, err = svc.RefreshAccountToken(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": at}})
			}
			require.NoError(t, err)
			require.Equal(t, live, svc.BuildAccountCredentials(info)["subscription_expires_at"])
		})
	}
}

type openaiOAuthClientRefreshStub struct {
	refreshCalls int32
}

func TestOpenAISubscriptionExpiryStoredPlan(t *testing.T) {
	for _, tc := range []struct{ id, wantPlan string }{{"mine", "pro"}, {"workspace", "plus"}} {
		t.Run(tc.id, func(t *testing.T) {
			expiry := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
			server := newChatGPTBackendTestServer(t, chatGPTBackendTestServerConfig{
				accountsCheck: map[string]any{"accounts": map[string]any{tc.id: map[string]any{
					"account":     map[string]any{"account_id": tc.id, "plan_type": "pro"},
					"entitlement": map[string]any{"expires_at": expiry},
				}}},
			})
			t.Cleanup(server.Close)
			client := &subscriptionOAuthClient{response: openai.TokenResponse{AccessToken: "fresh-access"}}
			svc := NewOpenAIOAuthService(nil, client)
			t.Cleanup(svc.Stop)
			svc.SetPrivacyClientFactory(newTestPrivacyClientFactory(server))
			for _, rt := range []string{"", "rt"} {
				account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
					"chatgpt_account_id": "mine", "plan_type": "plus", "access_token": "existing-access", "refresh_token": rt,
				}}
				info, err := svc.RefreshAccountToken(context.Background(), account)
				require.NoError(t, err)
				require.Equal(t, tc.wantPlan, info.PlanType)
				if tc.id == "mine" {
					require.Equal(t, expiry, info.SubscriptionExpiresAt)
				} else {
					require.Empty(t, info.SubscriptionExpiresAt)
				}
			}
		})
	}
}

func TestOpenAISubscriptionExpiryConflictingBearer(t *testing.T) {
	at := subscriptionJWT(t, map[string]any{"chatgpt_account_id": "other"})
	snapshot := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	id := subscriptionJWT(t, map[string]any{"chatgpt_account_id": "mine", "chatgpt_plan_type": "pro", "chatgpt_subscription_active_until": snapshot})
	client := &subscriptionOAuthClient{response: openai.TokenResponse{AccessToken: at, IDToken: id}}
	svc := NewOpenAIOAuthService(nil, client)
	t.Cleanup(svc.Stop)
	svc.SetPrivacyClientFactory(func(string) (*req.Client, error) {
		t.Error("conflicting identities must skip account lookups")
		return nil, errors.New("blocked")
	})
	info, err := svc.RefreshToken(context.Background(), "rt", "")
	require.ErrorContains(t, err, "reauthorization required")
	require.True(t, isNonRetryableRefreshError(err))
	require.Nil(t, info)
	for _, old := range []string{"", "2025-01-02T00:00:00Z"} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
			"chatgpt_account_id": "mine", "access_token": at, "plan_type": "plus", "subscription_expires_at": old,
		}}
		info, err := svc.RefreshAccountToken(context.Background(), account)
		require.ErrorContains(t, err, "reauthorization required")
		require.Nil(t, info)
		require.Equal(t, old, account.GetCredential("subscription_expires_at"))
	}
}

func (s *openaiOAuthClientRefreshStub) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientRefreshStub) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error) {
	atomic.AddInt32(&s.refreshCalls, 1)
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientRefreshStub) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string) (*openai.TokenResponse, error) {
	atomic.AddInt32(&s.refreshCalls, 1)
	return nil, errors.New("not implemented")
}

func TestOpenAIOAuthService_RefreshAccountToken_NoRefreshTokenUsesExistingAccessToken(t *testing.T) {
	client := &openaiOAuthClientRefreshStub{}
	svc := NewOpenAIOAuthService(nil, client)
	var privacyClientCalls int32
	svc.SetPrivacyClientFactory(func(proxyURL string) (*req.Client, error) {
		atomic.AddInt32(&privacyClientCalls, 1)
		return nil, errors.New("stop before request")
	})

	expiresAt := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "existing-access-token",
			"expires_at":   expiresAt,
			"client_id":    "client-id-1",
		},
	}

	info, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "existing-access-token", info.AccessToken)
	require.Equal(t, "client-id-1", info.ClientID)
	require.Zero(t, atomic.LoadInt32(&client.refreshCalls), "existing access token should be reused without calling refresh")
	require.Positive(t, atomic.LoadInt32(&privacyClientCalls), "existing access token should still run enrichment")
}

func TestOpenAIOAuthService_RefreshAccountToken_PATIgnoresStaleRefreshToken(t *testing.T) {
	client := &openaiOAuthClientRefreshStub{}
	var whoamiCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&whoamiCalls, 1)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"email":"user@example.com",
			"chatgpt_user_id":"user-123",
			"chatgpt_account_id":"acct-123",
			"chatgpt_plan_type":"plus",
			"chatgpt_account_is_fedramp":false
		}`))
	}))
	defer server.Close()

	originalURL := openAICodexPATWhoamiURL
	openAICodexPATWhoamiURL = server.URL
	defer func() { openAICodexPATWhoamiURL = originalURL }()

	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()

	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "at-test-token",
			"refresh_token": "stale-refresh-token",
			"expires_at":    time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"auth_mode":     "personal_access_token",
		},
	}

	info, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, OpenAIAuthModePersonalAccessToken, info.AuthMode)
	require.Equal(t, "at-test-token", info.AccessToken)
	require.Empty(t, info.RefreshToken)
	require.Equal(t, int32(1), atomic.LoadInt32(&whoamiCalls))
	require.Zero(t, atomic.LoadInt32(&client.refreshCalls), "PAT accounts must not call OAuth refresh even if stale refresh_token remains")
}

func TestOpenAITokenRefresher_NeedsRefresh_SkipsAccountWithoutRefreshToken(t *testing.T) {
	refresher := NewOpenAITokenRefresher(nil, nil)
	expiresAt := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)

	withoutRT := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   expiresAt,
		},
	}
	require.False(t, refresher.NeedsRefresh(withoutRT, 5*time.Minute))

	withRT := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}
	require.True(t, refresher.NeedsRefresh(withRT, 5*time.Minute))

	patWithStaleRT := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "at-test-token",
			"refresh_token": "stale-refresh-token",
			"expires_at":    expiresAt,
			"auth_mode":     OpenAIAuthModePersonalAccessToken,
		},
	}
	require.False(t, refresher.NeedsRefresh(patWithStaleRT, 5*time.Minute))
}

func TestOpenAITokenRefresher_Refresh_PATRemovesStaleOAuthFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"email":"user@example.com",
			"chatgpt_user_id":"user-123",
			"chatgpt_account_id":"acct-123",
			"chatgpt_plan_type":"plus",
			"chatgpt_account_is_fedramp":true
		}`))
	}))
	defer server.Close()

	originalURL := openAICodexPATWhoamiURL
	openAICodexPATWhoamiURL = server.URL
	defer func() { openAICodexPATWhoamiURL = originalURL }()

	svc := NewOpenAIOAuthService(nil, nil)
	defer svc.Stop()
	refresher := NewOpenAITokenRefresher(svc, nil)

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "at-test-token",
			"refresh_token": "stale-refresh-token",
			"id_token":      "stale-id-token",
			"expires_at":    time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"expires_in":    3600,
			"client_id":     "stale-client",
			"auth_mode":     OpenAIAuthModePersonalAccessToken,
			"model_mapping": map[string]any{"gpt-5": "gpt-5-codex"},
		},
	}

	credentials, err := refresher.Refresh(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "at-test-token", credentials["access_token"])
	require.Equal(t, OpenAIAuthModePersonalAccessToken, credentials["auth_mode"])
	require.Equal(t, "personal_access_token", credentials["openai_auth_mode"])
	require.NotContains(t, credentials, "refresh_token")
	require.NotContains(t, credentials, "id_token")
	require.NotContains(t, credentials, "expires_at")
	require.NotContains(t, credentials, "expires_in")
	require.NotContains(t, credentials, "client_id")
	require.Equal(t, map[string]any{"gpt-5": "gpt-5-codex"}, credentials["model_mapping"])
}

func TestOpenAITokenProvider_NoRefreshTokenExpiredAccessTokenReturnsError(t *testing.T) {
	provider := NewOpenAITokenProvider(nil, nil, nil)
	expiresAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "expired-access-token",
			"expires_at":   expiresAt,
		},
	}

	token, err := provider.GetAccessToken(context.Background(), account)
	require.Error(t, err)
	require.Empty(t, token)
	require.Contains(t, err.Error(), "refresh_token is missing")
}
