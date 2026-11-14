package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func pausedImportPrincipal() *adminaccess.Principal {
	return &adminaccess.Principal{KeyID: "paused-import-fixture", Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{
		Permissions: []string{"accounts.create", "accounts.credentials", "accounts.schedule"},
		Platforms:   []string{"openai"}, AccountTypes: []string{"oauth"}, GroupIDs: []int64{1},
	}}}}
}

func pausedImportConfig() *config.Config {
	cfg := &config.Config{}
	cfg.JWT.Secret = strings.Repeat("fixture-only-secret-", 3)
	return cfg
}

func pausedImportRouter(h *AccountHandler, principal **adminaccess.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if *principal != nil {
			c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), *principal))
		}
	})
	r.Use(h.ScopedAdminKeyAccounts)
	r.GET("/api/v1/admin/accounts/import/codex-session/capabilities", h.CodexSessionImportCapabilities)
	r.POST(pausedCodexImportPath, h.ImportCodexSessionPaused)
	r.POST("/api/v1/admin/accounts/import/codex-session", h.ImportCodexSession)
	return r
}

func pausedImportBody(scope string) map[string]any {
	return map[string]any{
		"content": `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","email":"paused@example.test"}`,
		"name":    "paused-fixture", "group_ids": []int64{1}, "create_paused": true,
		"contract_version": 1, "update_existing": false, "idempotency_scope": scope,
	}
}

func pausedImportCall(t *testing.T, r *gin.Engine, method, path, key string, body any, apiKey ...string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if len(apiKey) > 0 {
		req.Header.Set("x-api-key", apiKey[0])
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.NotContains(t, w.Body.String(), "synthetic-access")
	require.NotContains(t, w.Body.String(), "synthetic-refresh")
	return w
}

func TestPausedCodexImportCapabilitiesAndFailClosedContract(t *testing.T) {
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
	capability := result.Data
	require.True(t, capability.CreatePaused)
	require.Equal(t, 1, capability.ContractVersion)
	require.Equal(t, pausedCodexImportPath, capability.ImportPath)
	require.Regexp(t, "^[0-9a-f]{64}$", capability.IdempotencyScope)
	require.Equal(t, int64(86400), capability.IdempotencyTTLSeconds)
	require.NotContains(t, w.Body.String(), principal.KeyID)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"active forbidden", func(b map[string]any) { b["create_paused"] = false }},
		{"update forbidden", func(b map[string]any) { b["update_existing"] = true }},
		{"update default forbidden", func(b map[string]any) { delete(b, "update_existing") }},
		{"unsupported contract", func(b map[string]any) { b["contract_version"] = 2 }},
		{"missing scope", func(b map[string]any) { delete(b, "idempotency_scope") }},
		{"multiple groups", func(b map[string]any) { b["group_ids"] = []int64{1, 2} }},
		{"batch forbidden", func(b map[string]any) { b["content"] = "synthetic-access\nsecond-access" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := pausedImportBody(capability.IdempotencyScope)
			tc.mutate(body)
			w := pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
			require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
			require.Empty(t, memory.createdAccounts)
		})
	}
	body := pausedImportBody(capability.IdempotencyScope)
	w = pausedImportCall(t, r, "POST", pausedCodexImportPath, "", body)
	require.Equal(t, 400, w.Code)
	w = pausedImportCall(t, r, "POST", "/api/v1/admin/accounts/import/codex-session", "first-import", body)
	require.Equal(t, 400, w.Code, "guarded request cannot fall back to legacy import")
	first := pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
	require.Equal(t, 200, first.Code, first.Body.String())
	require.Len(t, memory.createdAccounts, 1)
	require.True(t, memory.createdAccounts[0].CreatePaused)
	require.Empty(t, memory.lookups, "create-only must not locate or update an existing identity")
	replay := pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), replay.Body.String())
	require.Len(t, memory.createdAccounts, 1)
	principal.KeyID = "rotated-key"
	w = pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
	require.Equal(t, 409, w.Code)
	principal = pausedImportPrincipal()
	principal.Policy.Rules[0].Permissions = append(principal.Policy.Rules[0].Permissions, "accounts.read")
	w = pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
	require.Equal(t, 409, w.Code)
	principal = pausedImportPrincipal()
	cfg := service.DefaultIdempotencyConfig()
	cfg.DefaultTTL = 48 * time.Hour
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	w = pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
	require.Equal(t, 409, w.Code, "TTL change invalidates the old replay boundary")
	service.SetDefaultIdempotencyCoordinator(nil)
	w = pausedImportCall(t, r, "POST", pausedCodexImportPath, "first-import", body)
	require.Equal(t, 503, w.Code, "missing idempotency coordinator must never execute the import")
	require.Len(t, memory.createdAccounts, 1)
}

func TestPausedCodexImportRequiresScopedCreationPermission(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	h := &AccountHandler{cfg: pausedImportConfig()}
	for _, principal := range []*adminaccess.Principal{nil, {KeyID: "full", Policy: adminaccess.Policy{FullAccess: true}}, {KeyID: "read", Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.read"}, AllGroups: true}}}}} {
		ctx := context.Background()
		if principal != nil {
			ctx = adminaccess.WithPrincipal(ctx, principal)
		}
		_, err := h.pausedCodexImportCapabilities(ctx)
		require.Error(t, err)
	}
	principal := pausedImportPrincipal()
	ctx := adminaccess.WithPrincipal(context.Background(), principal)
	first, err := h.pausedCodexImportCapabilities(ctx)
	require.NoError(t, err)
	second, err := (&AccountHandler{cfg: pausedImportConfig()}).pausedCodexImportCapabilities(ctx)
	require.NoError(t, err)
	require.Equal(t, first.IdempotencyScope, second.IdempotencyScope, "stable across restarts")
	h.cfg.JWT.Secret = strings.Repeat("rotated-secret-", 4)
	third, err := h.pausedCodexImportCapabilities(ctx)
	require.NoError(t, err)
	require.NotEqual(t, first.IdempotencyScope, third.IdempotencyScope)
}
