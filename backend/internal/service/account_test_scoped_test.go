//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/stretchr/testify/require"
)

type scopedTestAccountRepo struct {
	openAIAccountTestRepo
	account *Account
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
