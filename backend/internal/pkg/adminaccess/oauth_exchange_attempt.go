package adminaccess

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

const (
	CodexJobHeader             = "X-Codex-Job-Id"
	CodexExchangeOutcomeHeader = "X-Codex-Exchange-Outcome"
	CodexExchangeNotStarted    = "not-started"
)

// ExchangeAttemptID accepts only GW's UUIDv4 correlation format for the scoped exchange route.
func ExchangeAttemptID(ctx context.Context, method, route string, headers http.Header) string {
	if method != http.MethodPost || route != "/api/v1/admin/openai/exchange-code" {
		return ""
	}
	p := RestrictedFromContext(ctx)
	if p == nil || !RouteAllowed(p.Policy, method, route) {
		return ""
	}
	values := headers.Values(CodexJobHeader)
	if len(values) != 1 || len(values[0]) != 36 {
		return ""
	}
	id, err := uuid.Parse(values[0])
	if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != values[0] {
		return ""
	}
	return values[0]
}
