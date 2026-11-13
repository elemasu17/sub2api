package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) scopedAdminTest(c *gin.Context) {
	p := adminaccess.RestrictedFromContext(c.Request.Context())
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if p == nil || h.adminService == nil {
		response.Error(c, 403, "Account test is not permitted")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil || account == nil || !service.ScopedOpenAIAccountTestAllowed(p, account) {
		response.Error(c, 404, "Account not found")
		return
	}
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid account test request")
		return
	}
	model := ""
	for key, value := range fields {
		if key != "model_id" || json.Unmarshal(value, &model) != nil || len(model) > 128 {
			response.BadRequest(c, "Only an optional text model_id is supported")
			return
		}
	}
	if h.accountTestService == nil {
		response.Error(c, 503, "Account test service unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	// The service rechecks the resource after loading it and emits only a boolean
	// completion event. This branch deliberately never calls runtime recovery.
	_ = h.accountTestService.TestAccountConnection(c, id, model, "", service.AccountTestModeDefault)
}
