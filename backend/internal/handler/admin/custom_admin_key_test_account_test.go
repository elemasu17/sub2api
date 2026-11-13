//go:build unit

package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type scopedTestHandlerAdmin struct {
	service.AdminService
	account *service.Account
}

func (s *scopedTestHandlerAdmin) GetAccount(context.Context, int64) (*service.Account, error) {
	return s.account, nil
}

func TestScopedAccountTestMiddlewareDeniesOtherGroupsAndUnsupportedActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, platform, kind, body string
		group                      int64
		permission                 string
		status                     int
	}{
		{"foreign group", "openai", "oauth", `{}`, 99, "accounts.test", 404},
		{"missing permission", "openai", "oauth", `{}`, 7, "accounts.read", 404},
		{"other platform", "anthropic", "oauth", `{}`, 7, "accounts.test", 404},
		{"other type", "openai", "apikey", `{}`, 7, "accounts.test", 404},
		{"custom prompt", "openai", "oauth", `{"prompt":"PRIVATE"}`, 7, "accounts.test", 400},
		{"authorized reaches dedicated test", "openai", "oauth", `{}`, 7, "accounts.test", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &AccountHandler{adminService: &scopedTestHandlerAdmin{account: &service.Account{
				ID: 1, Platform: tc.platform, Type: tc.kind, GroupIDs: []int64{tc.group},
			}}}
			p := &adminaccess.Principal{KeyID: "key", Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{
				Permissions: []string{tc.permission}, GroupIDs: []int64{7}, AccountTypes: []string{"oauth"}, Platforms: []string{"openai"},
			}}}}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), p))
			})
			router.Use(h.ScopedAdminKeyAccounts)
			router.POST("/api/v1/admin/accounts/:id/test", func(c *gin.Context) { t.Fatal("must not reach unrestricted handler") })
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/admin/accounts/1/test", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			require.Equal(t, tc.status, recorder.Code)
			require.NotContains(t, recorder.Body.String(), "PRIVATE")
		})
	}
}
