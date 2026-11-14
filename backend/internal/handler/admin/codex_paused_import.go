package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const pausedCodexImportPath = "/api/v1/admin/accounts/import/codex-session/paused"

type codexImportCapabilities struct {
	CreatePaused          bool   `json:"create_paused"`
	ContractVersion       int    `json:"contract_version"`
	ImportPath            string `json:"import_path"`
	IdempotencyScope      string `json:"idempotency_scope"`
	IdempotencyTTLSeconds int64  `json:"idempotency_ttl_seconds"`
}

// This fingerprint is a retry boundary, not an ownership or authorization token.
// Permissions are loaded and checked again for every import and database write.
func (h *AccountHandler) pausedCodexImportCapabilities(ctx context.Context) (*codexImportCapabilities, error) {
	principal := adminaccess.RestrictedFromContext(ctx)
	if principal == nil || principal.KeyID == "" || !adminaccess.RouteAllowed(principal.Policy, "POST", pausedCodexImportPath) {
		return nil, infraerrors.Forbidden("SCOPED_CODEX_FORBIDDEN", "Paused import requires a scoped account creation key")
	}
	if h.cfg == nil || len(h.cfg.JWT.Secret) < 32 || service.DefaultIdempotencyCoordinator() == nil {
		return nil, infraerrors.ServiceUnavailable("SCOPED_CODEX_UNAVAILABLE", "Paused import is unavailable")
	}
	ttl := service.DefaultWriteIdempotencyTTL()
	material, err := json.Marshal(struct {
		KeyID  string
		Policy adminaccess.Policy
		TTL    int64
	}{principal.KeyID, principal.Policy, int64(ttl)})
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SCOPED_CODEX_UNAVAILABLE", "Paused import is unavailable")
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.JWT.Secret))
	_, _ = mac.Write([]byte("sub2api.codex-paused-import.v1\x00"))
	_, _ = mac.Write(material)
	return &codexImportCapabilities{
		CreatePaused: true, ContractVersion: 1, ImportPath: pausedCodexImportPath,
		IdempotencyScope: hex.EncodeToString(mac.Sum(nil)), IdempotencyTTLSeconds: int64(ttl / time.Second),
	}, nil
}

func validatePausedCodexImport(req scopedCodexSessionImportRequest, key string, capability *codexImportCapabilities) error {
	if !req.CreatePaused || req.ContractVersion != capability.ContractVersion || req.UpdateExisting == nil || *req.UpdateExisting || len(req.GroupIDs) != 1 || req.GroupIDs[0] <= 0 {
		return infraerrors.BadRequest("SCOPED_CODEX_PAUSED_CONTRACT", "Paused import requires version 1, create_paused=true, update_existing=false and one group")
	}
	if len(req.IdempotencyScope) != 64 || !hmac.Equal([]byte(req.IdempotencyScope), []byte(capability.IdempotencyScope)) {
		return infraerrors.Conflict("SCOPED_CODEX_IDEMPOTENCY_SCOPE_CHANGED", "Import scope changed; reconcile before retrying")
	}
	normalized, err := service.NormalizeIdempotencyKey(key)
	if err != nil {
		return err
	}
	if normalized == "" {
		return service.ErrIdempotencyKeyRequired
	}
	return nil
}

// CodexSessionImportCapabilities advertises only the guarded, paused-only path.
func (h *AccountHandler) CodexSessionImportCapabilities(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	capability, err := h.pausedCodexImportCapabilities(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, capability)
}

// ImportCodexSessionPaused is separate from the legacy route so a downgrade
// between capability discovery and POST cannot silently create an active row.
func (h *AccountHandler) ImportCodexSessionPaused(c *gin.Context) {
	h.scopedAdminImportCodexSessionMode(c, true)
}
