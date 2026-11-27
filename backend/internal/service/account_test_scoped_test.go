//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/stretchr/testify/require"
)

type scopedTestAccountRepo struct {
	openAIAccountTestRepo
	account *Account
}

func TestScopedAccountTestCredentialFailure(t *testing.T) {
	const rejection = `{"error":{"code":"token_invalidated","message":"PRIVATE upstream response"}}`
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		attested bool
	}{
		{"invalidated", 401, rejection, true},
		{"revoked", 401, `{"error":{"code":"token_revoked"}}`, true},
		{"forbidden", 403, rejection, false},
		{"rate limit", 429, rejection, false},
		{"server", 502, rejection, false},
		{"plain text", 401, "token_invalidated", false},
		{"diagnostic text", 401, `{"error":{"message":"token_revoked"}}`, false},
		{"expiry", 401, `{"error":{"code":"token_expired"}}`, false},
		{"truncated", 401, rejection + "{", false},
		{"oversize", 401, rejection + strings.Repeat(" ", 64<<10), false},
		{"SSE text", 200, "data: " + rejection + "\n\n", false},
		{"transport", 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{7}, Status: StatusActive,
				Credentials: map[string]any{"access_token": "PRIVATE test fixture"}}
			repo := &scopedTestAccountRepo{account: a}
			upstream := &queuedHTTPUpstream{}
			if tc.status != 0 {
				upstream.responses = []*http.Response{newJSONResponse(tc.status, tc.body)}
			}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
			ctx, rec := newTestContext()
			ctx.Request = ctx.Request.WithContext(adminaccess.WithPrincipal(ctx.Request.Context(), scopedTestPrincipal()))
			err := svc.TestAccountConnection(ctx, 1, "", "", AccountTestModeDefault)
			require.EqualError(t, err, "Account connectivity test failed")
			want := `{"type":"test_complete","success":false,"code":"ACCOUNT_TEST_FAILED"}`
			if tc.attested {
				want = `{"type":"test_complete","success":false,"code":"ACCOUNT_TEST_FAILED","error_code":"upstream_credential_invalid"}`
			}
			require.JSONEq(t, want, strings.TrimPrefix(strings.TrimSpace(rec.Body.String()), "data: "))
			require.Len(t, upstream.requests, 1)
			require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
			require.False(t, a.Schedulable)
			require.Equal(t, StatusActive, a.Status)
			require.Zero(t, repo.setErrorID)
			require.Zero(t, repo.clearedErrorID)
			require.Zero(t, repo.rateLimitedID)
			require.Empty(t, repo.updatedExtra)
		})
	}
}

func (r *scopedTestAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func scopedTestPrincipal() *adminaccess.Principal {
	return &adminaccess.Principal{KeyID: "scoped-key", Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{
		Permissions: []string{"accounts.test"}, GroupIDs: []int64{7}, AccountTypes: []string{"oauth"}, Platforms: []string{"openai"},
	}}}}
}

func TestScopedAccountTestRedactsStreamAndDoesNotMutateAccount(t *testing.T) {
	for _, code := range []int{200, 401, 429} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			body := `data: {"type":"response.output_text.delta","delta":"UPSTREAM_SECRET"}` + "\n\n" + `data: {"type":"response.completed"}` + "\n\n"
			if code != 200 {
				body = `{"error":"UPSTREAM_SECRET","reset_at":1999999999}`
			}
			resp := newJSONResponse(code, body)
			resp.Header.Set("x-codex-primary-used-percent", "88")
			resp.Header.Set("x-codex-primary-reset-after-seconds", "3600")
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{7},
				Status: StatusError, Concurrency: 1, Credentials: map[string]any{"access_token": "ACCOUNT_SECRET"}}
			repo := &scopedTestAccountRepo{account: account}
			upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
			ctx, recorder := newTestContext()
			ctx.Request = ctx.Request.WithContext(adminaccess.WithPrincipal(ctx.Request.Context(), scopedTestPrincipal()))
			err := svc.TestAccountConnection(ctx, 1, "gpt-5.4", "", AccountTestModeDefault)
			if code == 200 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NotContains(t, recorder.Body.String(), "SECRET")
			require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
			if code == 200 {
				require.Contains(t, recorder.Body.String(), `"success":true`)
			} else {
				require.Contains(t, recorder.Body.String(), `"success":false`)
			}
			require.Zero(t, repo.setErrorID)
			require.Zero(t, repo.clearedErrorID)
			require.Zero(t, repo.rateLimitedID)
			require.Empty(t, repo.updatedExtra)
			require.Equal(t, StatusError, account.Status)
		})
	}
}

func TestScopedAccountTestRechecksScopeBeforeUpstream(t *testing.T) {
	for _, account := range []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{99}},
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, GroupIDs: []int64{7}},
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: []int64{7}},
	} {
		repo := &scopedTestAccountRepo{account: account}
		svc := &AccountTestService{accountRepo: repo}
		ctx, recorder := newTestContext()
		ctx.Request = ctx.Request.WithContext(adminaccess.WithPrincipal(ctx.Request.Context(), scopedTestPrincipal()))
		require.Error(t, svc.TestAccountConnection(ctx, 1, "", "", ""))
		require.Contains(t, recorder.Body.String(), `"success":false`)
	}
}

func TestScopedAccountTestRequiresAccountOwnedOAuthEvidence(t *testing.T) {
	for _, name := range []string{"personal access token", "blocked account header override", "failed plugin binding"} {
		t.Run(name, func(t *testing.T) {
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{7},
				Credentials: map[string]any{"access_token": "fixture", "chatgpt_account_id": "target"}}
			upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(401, `{"error":{"code":"token_revoked"}}`)}}
			svc := &AccountTestService{httpUpstream: upstream, accountRepo: &scopedTestAccountRepo{account: account}}
			switch name {
			case "personal access token":
				account.Credentials[openAIAuthModeCredentialKey] = OpenAIAuthModePersonalAccessToken
			case "blocked account header override":
				account.Credentials[credKeyHeaderOverrideEnabled] = true
				account.Credentials[credKeyHeaderOverrides] = map[string]any{"chatgpt-account-id": "other"}
			case "failed plugin binding":
				svc.pluginManager = &PluginManager{}
				svc.pluginManager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "token_revoked"})
			}
			ctx, rec := newTestContext()
			ctx.Request = ctx.Request.WithContext(adminaccess.WithPrincipal(ctx.Request.Context(), scopedTestPrincipal()))
			require.Error(t, svc.TestAccountConnection(ctx, 1, "", "", AccountTestModeDefault))
			want := `{"type":"test_complete","success":false,"code":"ACCOUNT_TEST_FAILED"}`
			if name == "blocked account header override" {
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "target", upstream.requests[0].Header.Get("chatgpt-account-id"))
				want = `{"type":"test_complete","success":false,"code":"ACCOUNT_TEST_FAILED","error_code":"upstream_credential_invalid"}`
			}
			require.JSONEq(t, want, strings.TrimPrefix(strings.TrimSpace(rec.Body.String()), "data: "))
			if name == "failed plugin binding" {
				require.Empty(t, upstream.requests)
			}
		})
	}
}
