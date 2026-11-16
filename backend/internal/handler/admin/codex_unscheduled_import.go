package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const unscheduledCodexImportPath = "/api/v1/admin/accounts/import/codex-session/unscheduled"

type activeUnscheduledCodexImportCapability struct {
	ContractVersion       int    `json:"contract_version"`
	ImportPath            string `json:"import_path"`
	Status                string `json:"status"`
	Schedulable           bool   `json:"schedulable"`
	IdempotencyScope      string `json:"idempotency_scope"`
	IdempotencyTTLSeconds int64  `json:"idempotency_ttl_seconds"`
}

func (h *AccountHandler) activeUnscheduledCodexImportCapabilities(ctx context.Context) (*activeUnscheduledCodexImportCapability, error) {
	legacy, err := h.pausedCodexImportCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	principal := adminaccess.RestrictedFromContext(ctx)
	if !adminaccess.RouteAllowed(principal.Policy, "POST", unscheduledCodexImportPath) {
		return nil, infraerrors.Forbidden("SCOPED_CODEX_FORBIDDEN", "Unscheduled import requires a scoped account creation key")
	}
	// Bind the new namespace to exactly the same key, policy, TTL and server secret
	// as v1, without changing the old capability's scope or replay behavior.
	mac := hmac.New(sha256.New, []byte(h.cfg.JWT.Secret))
	_, _ = mac.Write([]byte("sub2api.codex-unscheduled-import.v2\x00"))
	_, _ = mac.Write([]byte(legacy.IdempotencyScope))
	return &activeUnscheduledCodexImportCapability{ContractVersion: 2, ImportPath: unscheduledCodexImportPath,
		Status: service.StatusActive, Schedulable: false, IdempotencyScope: hex.EncodeToString(mac.Sum(nil)),
		IdempotencyTTLSeconds: legacy.IdempotencyTTLSeconds}, nil
}

func validateActiveUnscheduledCodexImport(req scopedCodexSessionImportRequest, key string, capability *activeUnscheduledCodexImportCapability) error {
	if !req.CreateUnscheduled || req.CreatePaused || req.ContractVersion != 2 || req.UpdateExisting == nil || *req.UpdateExisting || len(req.GroupIDs) != 1 || req.GroupIDs[0] <= 0 {
		return infraerrors.BadRequest("SCOPED_CODEX_UNSCHEDULED_CONTRACT", "Unscheduled import requires version 2, create_unscheduled=true, update_existing=false and one group")
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

// A separate route prevents old binaries from ignoring the new creation mode.
func (h *AccountHandler) ImportCodexSessionUnscheduled(c *gin.Context) {
	h.scopedAdminImportCodexSessionMode(c, "unscheduled")
}
