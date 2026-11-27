package service

import (
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/tidwall/gjson"
)

const scopedTestCredentialInvalid = "upstream_credential_invalid"

func scopedOpenAICredentialRejected(resp *http.Response) bool {
	if resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	const maxErrorBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes+1))
	if err != nil || len(body) > maxErrorBytes || !gjson.ValidBytes(body) {
		return false
	}
	switch gjson.GetBytes(body, "error.code").String() {
	case "token_invalidated", "token_revoked":
		return true
	}
	return false
}

// ScopedOpenAIAccountTestAllowed excludes aliases and signed Agent Identity
// flows, which can access parent credentials or register replacement tasks.
func ScopedOpenAIAccountTestAllowed(p *adminaccess.Principal, account *Account) bool {
	return p != nil && account != nil && account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth &&
		!account.IsShadow() && !account.IsOpenAIAgentIdentity() && !account.IsSyntheticUITest() &&
		p.Policy.AllowsAccount("accounts.test", adminaccess.Account{
			ID: account.ID, GroupIDs: account.GroupIDs, Platform: account.Platform, Type: account.Type,
		})
}
