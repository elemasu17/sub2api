package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"

// ScopedOpenAIAccountTestAllowed excludes aliases and signed Agent Identity
// flows, which can access parent credentials or register replacement tasks.
func ScopedOpenAIAccountTestAllowed(p *adminaccess.Principal, account *Account) bool {
	return p != nil && account != nil && account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth &&
		!account.IsShadow() && !account.IsOpenAIAgentIdentity() && !account.IsSyntheticUITest() &&
		p.Policy.AllowsAccount("accounts.test", adminaccess.Account{
			ID: account.ID, GroupIDs: account.GroupIDs, Platform: account.Platform, Type: account.Type,
		})
}
