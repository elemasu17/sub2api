package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func unscheduledImportBody(scope string) map[string]any {
	body := pausedImportBody(scope)
	delete(body, "create_paused")
	body["create_unscheduled"] = true
	body["contract_version"] = 2
	return body
}

func TestUnscheduledCodexImportPreservesV1RequestFingerprint(t *testing.T) {
	// The coordinator hashes the normalized request. Adding an always-present
	// false field would invalidate durable v1 replay records after an upgrade.
	type legacyRequest struct {
		CodexSessionImportRequest
		ProxyMode        string `json:"proxy_mode"`
		RandomProxy      bool   `json:"random_proxy"`
		CreatePaused     bool   `json:"create_paused"`
		ContractVersion  int    `json:"contract_version"`
		IdempotencyScope string `json:"idempotency_scope"`
	}
	raw, err := json.Marshal(pausedImportBody(strings.Repeat("a", 64)))
	require.NoError(t, err)
	var legacy legacyRequest
	var current scopedCodexSessionImportRequest
	require.NoError(t, json.Unmarshal(raw, &legacy))
	require.NoError(t, json.Unmarshal(raw, &current))
	oldNormalized, err := json.Marshal(legacy)
	require.NoError(t, err)
	newNormalized, err := json.Marshal(current)
	require.NoError(t, err)
	require.Equal(t, string(oldNormalized), string(newNormalized))
}

func TestUnscheduledCodexImportCapabilitiesAndFailClosedContract(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	memory := &scopedCodexMemoryService{codexImportMemoryAdminService: newCodexImportMemoryAdminService(nil)}
	h := &AccountHandler{cfg: pausedImportConfig(), adminService: memory}
	principal := pausedImportPrincipal()
	r := pausedImportRouter(h, &principal)
	w := pausedImportCall(t, r, "GET", "/api/v1/admin/accounts/import/codex-session/capabilities", "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var result struct {
		Data codexImportCapabilities `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	capability := result.Data.ActiveUnscheduled
	require.NotNil(t, capability)
	require.NotEqual(t, result.Data.IdempotencyScope, capability.IdempotencyScope)
	require.Equal(t, service.StatusActive, capability.Status)
	require.False(t, capability.Schedulable)
	require.Equal(t, 2, capability.ContractVersion)
	require.Equal(t, unscheduledCodexImportPath, capability.ImportPath)
	require.Regexp(t, "^[0-9a-f]{64}$", capability.IdempotencyScope)
	require.Equal(t, int64(86400), capability.IdempotencyTTLSeconds)
	require.NotContains(t, w.Body.String(), principal.KeyID)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"scheduling mode required", func(b map[string]any) { b["create_unscheduled"] = false }},
		{"conflicting paused mode", func(b map[string]any) { b["create_paused"] = true }},
		{"update forbidden", func(b map[string]any) { b["update_existing"] = true }},
		{"update default forbidden", func(b map[string]any) { delete(b, "update_existing") }},
		{"unsupported contract", func(b map[string]any) { b["contract_version"] = 1 }},
		{"missing scope", func(b map[string]any) { delete(b, "idempotency_scope") }},
		{"multiple groups", func(b map[string]any) { b["group_ids"] = []int64{1, 2} }},
		{"batch forbidden", func(b map[string]any) { b["content"] = "synthetic-access\nsecond-access" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := unscheduledImportBody(capability.IdempotencyScope)
			tc.mutate(body)
			w := pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "first-import", body)
			require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
			require.Empty(t, memory.createdAccounts)
		})
	}
	body := unscheduledImportBody(capability.IdempotencyScope)
	w = pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "", body)
	require.Equal(t, 400, w.Code)
	w = pausedImportCall(t, r, "POST", "/api/v1/admin/accounts/import/codex-session", "first-import", body)
	require.Equal(t, 400, w.Code, "guarded request cannot fall back to legacy import")
	w = pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
	require.Equal(t, 400, w.Code, "v2 must not fall back to v1")
	first := pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "first-import", body)
	require.Equal(t, 200, first.Code, first.Body.String())
	require.Len(t, memory.createdAccounts, 1)
	require.True(t, memory.createdAccounts[0].CreateUnscheduled)
	require.Empty(t, memory.lookups, "create-only must not locate or update an existing identity")
	replay := pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "first-import", body)
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), replay.Body.String())
	require.Len(t, memory.createdAccounts, 1)
	principal.KeyID = "rotated-key"
	w = pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "first-import", body)
	require.Equal(t, 409, w.Code)
	principal = pausedImportPrincipal()
	principal.Policy.Rules[0].Permissions = append(principal.Policy.Rules[0].Permissions, "accounts.read")
	w = pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "first-import", body)
	require.Equal(t, 409, w.Code)
	principal = pausedImportPrincipal()
	cfg := service.DefaultIdempotencyConfig()
	cfg.DefaultTTL = 48 * time.Hour
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	w = pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "first-import", body)
	require.Equal(t, 409, w.Code, "TTL change invalidates the old replay boundary")
	service.SetDefaultIdempotencyCoordinator(nil)
	w = pausedImportCall(t, r, "POST", unscheduledCodexImportPath, "first-import", body)
	require.Equal(t, 503, w.Code, "missing idempotency coordinator must never execute the import")
	require.Len(t, memory.createdAccounts, 1)
}

func TestUnscheduledCodexImportRequiresScopedCreationPermission(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	h := &AccountHandler{cfg: pausedImportConfig()}
	for _, principal := range []*adminaccess.Principal{nil, {KeyID: "full", Policy: adminaccess.Policy{FullAccess: true}}, {KeyID: "read", Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.read"}, AllGroups: true}}}}} {
		ctx := context.Background()
		if principal != nil {
			ctx = adminaccess.WithPrincipal(ctx, principal)
		}
		_, err := h.activeUnscheduledCodexImportCapabilities(ctx)
		require.Error(t, err)
	}
	principal := pausedImportPrincipal()
	ctx := adminaccess.WithPrincipal(context.Background(), principal)
	first, err := h.activeUnscheduledCodexImportCapabilities(ctx)
	require.NoError(t, err)
	second, err := (&AccountHandler{cfg: pausedImportConfig()}).activeUnscheduledCodexImportCapabilities(ctx)
	require.NoError(t, err)
	require.Equal(t, first.IdempotencyScope, second.IdempotencyScope, "stable across restarts")
	h.cfg.JWT.Secret = strings.Repeat("rotated-secret-", 4)
	third, err := h.activeUnscheduledCodexImportCapabilities(ctx)
	require.NoError(t, err)
	require.NotEqual(t, first.IdempotencyScope, third.IdempotencyScope)
}
