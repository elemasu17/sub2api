package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func scopedOAuthContext(key string, permissions ...string) context.Context {
	return adminaccess.WithPrincipal(context.Background(), &adminaccess.Principal{
		KeyID: key, Policy: adminaccess.Policy{GlobalPermissions: permissions},
	})
}

func TestScopedOpenAIOAuthRejectsMissingPermissionAndUnsafeParameters(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, nil)
	defer svc.Stop()
	_, err := svc.GenerateAuthURL(scopedOAuthContext("key-1"), nil, "", "openai")
	require.Equal(t, 403, infraerrors.Code(err))
	ctx := scopedOAuthContext("key-1", "openai.oauth.authorize")
	for _, redirect := range []string{
		"https://example.com/callback", "http://localhost.evil:1455/auth/callback",
		"http://localhost:1455/auth/callback?next=https://example.com", "http://localhost:1456/auth/callback",
		"http://127.0.0.1:1455/other", "http://user@localhost:1455/auth/callback",
	} {
		_, err := svc.GenerateAuthURL(ctx, nil, redirect, "openai")
		require.Equal(t, 400, infraerrors.Code(err), redirect)
	}
	proxy := int64(1)
	_, err = svc.GenerateAuthURL(ctx, &proxy, "", "openai")
	require.Equal(t, 403, infraerrors.Code(err))
	_, err = svc.GenerateAuthURL(ctx, nil, "", "other-provider")
	require.Equal(t, 403, infraerrors.Code(err))
}

func TestScopedOpenAIOAuthSessionIsBoundToKeyAndPolicyAndConsumedOnce(t *testing.T) {
	client := &openaiOAuthClientStateStub{}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	ctx := scopedOAuthContext("key-1", "openai.oauth.authorize")
	flow, err := svc.GenerateAuthURL(ctx, nil, "http://127.0.0.1:1455/auth/callback", "openai")
	require.NoError(t, err)
	require.NotEmpty(t, flow.State)
	input := &OpenAIExchangeCodeInput{SessionID: flow.SessionID, Code: "code", State: flow.State}
	for _, wrong := range []context.Context{
		context.Background(), scopedOAuthContext("key-2", "openai.oauth.authorize"),
		scopedOAuthContext("key-1", "openai.oauth.authorize", "system.read"), scopedOAuthContext("key-1"),
	} {
		_, err := svc.ExchangeCode(wrong, input)
		require.Equal(t, 403, infraerrors.Code(err))
	}
	input.RedirectURI = openai.DefaultRedirectURI
	_, err = svc.ExchangeCode(ctx, input)
	require.Equal(t, 403, infraerrors.Code(err), "even another allowed callback cannot override the session")
	input.RedirectURI = ""
	proxy := int64(2)
	input.ProxyID = &proxy
	_, err = svc.ExchangeCode(ctx, input)
	require.Equal(t, 403, infraerrors.Code(err))
	input.ProxyID = nil
	require.Zero(t, client.exchangeCalled)
	info, err := svc.ExchangeCode(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "at", info.AccessToken)
	_, err = svc.ExchangeCode(ctx, input)
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, int32(1), client.exchangeCalled)
}

func TestScopedOpenAIOAuthCannotUseInteractiveAdminSession(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, nil)
	defer svc.Stop()
	flow, err := svc.GenerateAuthURL(context.Background(), nil, "", "openai")
	require.NoError(t, err)
	_, err = svc.ExchangeCode(scopedOAuthContext("key-1", "openai.oauth.authorize"), &OpenAIExchangeCodeInput{
		SessionID: flow.SessionID, Code: "code", State: flow.State,
	})
	require.Equal(t, 403, infraerrors.Code(err))
}
