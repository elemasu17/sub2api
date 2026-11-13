//go:build unit

package adminaccess

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestScopedOpenAILifecyclePermissionsAreNarrow(t *testing.T) {
	authorize := Policy{GlobalPermissions: []string{"openai.oauth.authorize"}}
	probe := Policy{Rules: []Rule{{Permissions: []string{"accounts.test"}, GroupIDs: []int64{7}, AccountTypes: []string{"oauth"}, Platforms: []string{"openai"}}}}
	require.NoError(t, ValidatePolicy(authorize))
	require.NoError(t, ValidatePolicy(probe))
	for _, path := range []string{"/openai/generate-auth-url", "/openai/exchange-code"} {
		require.True(t, RouteAllowed(authorize, "POST", "/api/v1/admin"+path))
		require.False(t, RouteAllowed(probe, "POST", "/api/v1/admin"+path))
		require.False(t, RouteAllowed(Policy{}, "POST", "/api/v1/admin"+path))
	}
	require.True(t, RouteAllowed(probe, "POST", "/api/v1/admin/accounts/:id/test"))
	require.False(t, RouteAllowed(authorize, "POST", "/api/v1/admin/accounts/:id/test"))
	for _, p := range []Policy{authorize, probe} {
		for _, path := range []string{"/accounts/:id/refresh", "/accounts/:id/clear-error", "/accounts/:id/recover-state", "/accounts/:id/schedulable", "/openai/refresh-token", "/accounts/generate-auth-url", "/openai/codex-pat/create"} {
			require.False(t, RouteAllowed(p, "POST", "/api/v1/admin"+path), path)
		}
		require.False(t, RouteAllowed(p, "PUT", "/api/v1/admin/accounts/:id"))
		require.False(t, RouteAllowed(p, "DELETE", "/api/v1/admin/accounts/:id"))
	}
}
