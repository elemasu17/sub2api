package adminaccess

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPausedCodexRoutesUseExistingCreatePermission(t *testing.T) {
	policy := Policy{Rules: []Rule{{Permissions: []string{"accounts.create"}, GroupIDs: []int64{1}, AccountTypes: []string{"oauth"}, Platforms: []string{"openai"}}}}
	require.True(t, RouteAllowed(policy, "GET", "/api/v1/admin/accounts/import/codex-session/capabilities"))
	require.True(t, RouteAllowed(policy, "POST", "/api/v1/admin/accounts/import/codex-session/paused"))
	require.False(t, RouteAllowed(policy, "DELETE", "/api/v1/admin/accounts/:id"))
	require.False(t, policy.AllowsAccount("accounts.create", Account{GroupIDs: []int64{2}, Type: "oauth", Platform: "openai"}))
	policy.Rules[0].Permissions = []string{"accounts.read"}
	require.False(t, RouteAllowed(policy, "GET", "/api/v1/admin/accounts/import/codex-session/capabilities"))
	require.False(t, RouteAllowed(policy, "POST", "/api/v1/admin/accounts/import/codex-session/paused"))
}
