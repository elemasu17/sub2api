//go:build unit

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNativeReleaseBlocksUpdateEndpointsButAllowsChecking(t *testing.T) {
	t.Setenv("ALLOW_OFFICIAL_BINARY_UPDATES", "")
	gin.SetMode(gin.TestMode)
	stub := &systemHandlerUpdateServiceStub{updateInfo: &service.UpdateInfo{
		CurrentVersion: "0.2.21-aibj.1", LatestVersion: "0.2.22", HasUpdate: true,
	}}
	h := NewSystemHandler(stub, nil)
	router := gin.New()
	router.POST("/update", h.PerformUpdate)
	router.POST("/rollback", h.Rollback)
	router.GET("/check-updates", h.CheckUpdates)
	for _, tc := range []struct{ path, body string }{
		{"/update", ""}, {"/rollback", ""}, {"/rollback", `{"version":"0.2.20"}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), "OFFICIAL_BINARY_UPDATES_DISABLED")
	}
	require.Zero(t, stub.performCall)
	require.Zero(t, stub.rollbackCall)
	require.Zero(t, stub.rollbackToCall)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/check-updates?force=true", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "0.2.22")
	require.Equal(t, []bool{true}, stub.checkForces)
}
