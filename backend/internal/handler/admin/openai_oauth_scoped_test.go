//go:build unit

package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestScopedOpenAIOAuthHTTPRejectsMalformedAndUnsafeInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.NewOpenAIOAuthService(nil, nil)
	defer svc.Stop()
	h := &OpenAIOAuthHandler{openaiOAuthService: svc}
	for _, tc := range []struct {
		body       string
		authorized bool
		status     int
	}{
		{`{}`, true, 200}, {`{}`, false, 403},
		{`{"redirect_uri":"https://example.com/callback"}`, true, 400},
		{`{"proxy_id":9}`, true, 403}, {`{"redirect_uri":`, true, 400},
	} {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			permissions := []string{}
			if tc.authorized {
				permissions = append(permissions, "openai.oauth.authorize")
			}
			c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{
				KeyID: "key", Policy: adminaccess.Policy{GlobalPermissions: permissions},
			}))
		})
		router.POST("/api/v1/admin/openai/generate-auth-url", h.GenerateAuthURL)
		req := httptest.NewRequest("POST", "/api/v1/admin/openai/generate-auth-url", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
		if tc.status == 200 {
			require.Contains(t, recorder.Body.String(), `"session_id"`)
			require.Contains(t, recorder.Body.String(), `"state"`)
			require.NotContains(t, recorder.Body.String(), `"code_verifier"`)
		}
	}
}
