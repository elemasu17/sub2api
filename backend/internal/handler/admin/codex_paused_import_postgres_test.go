package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Only network boundaries are replaced: HTTP handlers, service, transactions,
// repositories, and PostgreSQL are real. These stubs never open a connection.
type pausedImportOAuthFixture struct {
	service.OpenAIOAuthClient
	calls int
}

func (f *pausedImportOAuthFixture) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	f.calls++
	return &openai.TokenResponse{AccessToken: "refreshed-fixture-access", RefreshToken: "refreshed-fixture-refresh", ExpiresIn: 3600}, nil
}

type pausedImportModelFixture struct {
	service.HTTPUpstream
	status, calls int
}

func (f *pausedImportModelFixture) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	f.calls++
	body := "data: {\"type\":\"response.completed\"}\n\n"
	if f.status != 200 {
		body = `{"error":"PRIVATE_FIXTURE_UPSTREAM_ERROR"}`
	}
	return &http.Response{StatusCode: f.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

// Uses an isolated synthetic schema and the actual HTTP -> service -> repository
// chain. No upstream OAuth or model request is needed or allowed by this flow.
func TestPausedCodexImportPostgresInitialRowAndReplay(t *testing.T) {
	dsn := os.Getenv("SCOPED_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("requires independent SCOPED_ADMIN_TEST_DSN")
	}
	base, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("paused_codex_%d", time.Now().UnixNano())
	_, err = base.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn+" search_path="+schema)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close(); _, _ = base.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = base.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	// This change has no migrations. Use the current generated schema; replaying
	// unrelated historical migrations is a separate upgrade/bootstrap check.
	require.NoError(t, client.Schema.Create(context.Background()))
	// Raw-SQL repository helpers use migration-defined defaults/outbox that Ent
	// does not declare. Apply only those fixture components, unchanged.
	_, err = db.Exec("DROP TABLE idempotency_records")
	require.NoError(t, err)
	for _, filename := range []string{"036_scheduler_outbox.sql", "057_add_idempotency_records.sql", "066_add_scheduled_test_tables.sql", "152_scheduler_outbox_dedup_key.sql", "153_scheduler_outbox_pending_dedup_key_index_notx.sql", "247_scoped_admin_api_keys.sql"} {
		ddl, err := migrations.FS.ReadFile(filename)
		require.NoError(t, err)
		_, err = db.Exec(string(ddl))
		require.NoError(t, err)
	}
	// UserService hydrates avatars during real admin authentication. Reuse that
	// table/index DDL without running unrelated default-grant seed statements.
	avatarDDL, err := migrations.FS.ReadFile("110_pending_auth_and_provider_default_grants.sql")
	require.NoError(t, err)
	avatarStart := strings.Index(string(avatarDDL), "CREATE TABLE IF NOT EXISTS user_avatars")
	avatarEnd := strings.Index(string(avatarDDL), "INSERT INTO settings")
	require.GreaterOrEqual(t, avatarStart, 0)
	require.Greater(t, avatarEnd, avatarStart)
	_, err = db.Exec(string(avatarDDL[avatarStart:avatarEnd]))
	require.NoError(t, err)
	groups := repository.NewAdminGroupRepository(client, db)
	accounts := repository.NewAdminAccountRepository(client, db, nil)
	unused := &service.Group{Name: "unrelated-fixture", Platform: "openai", Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard}
	require.NoError(t, groups.Create(context.Background(), unused))
	group := &service.Group{Name: "codex-fixture", Platform: "openai", Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard}
	require.NoError(t, groups.Create(context.Background(), group))
	require.Equal(t, int64(2), group.ID)
	// Exact final policy: accounts.schedule is required even for the import's
	// default concurrency/priority; group reassignment is deliberately absent.
	principal := &adminaccess.Principal{KeyID: "paused-import-fixture", Policy: adminaccess.Policy{
		GlobalPermissions: []string{"openai.oauth.authorize"},
		Rules: []adminaccess.Rule{{
			Permissions: []string{"accounts.read", "accounts.create", "accounts.credentials", "accounts.schedule", "accounts.status", "accounts.refresh", "accounts.delete", "accounts.test"},
			GroupIDs:    []int64{2}, AllGroups: false, IncludeUngrouped: false,
			AccountTypes: []string{"oauth"}, Platforms: []string{"openai"},
		}},
	}}
	require.NoError(t, adminaccess.ValidatePolicy(principal.Policy))
	if path := os.Getenv("SCOPED_ADMIN_POLICY_PATH"); path != "" {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		var external struct {
			After adminaccess.Policy `json:"after"`
		}
		require.NoError(t, json.Unmarshal(content, &external))
		require.Equal(t, principal.Policy, external.After, "operator artifact and tested final policy must match exactly")
		principal.Policy = external.After
	}
	policyJSON, err := json.Marshal(principal.Policy)
	require.NoError(t, err)
	t.Logf("validated final policy SHA256: %x", sha256.Sum256(policyJSON))
	cfg := pausedImportConfig()
	admin := service.NewAdminService(cfg, nil, groups, accounts, nil, nil, nil, nil, nil, nil, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	oauth := &pausedImportOAuthFixture{}
	model := &pausedImportModelFixture{}
	h := &AccountHandler{cfg: cfg, adminService: admin,
		openaiOAuthService: service.NewOpenAIOAuthService(nil, oauth),
		accountTestService: service.NewAccountTestService(accounts, nil, nil, nil, nil, model, cfg, nil),
	}
	users := repository.NewUserRepository(client, db)
	owner := &service.User{Email: "operator@example.test", PasswordHash: "unused-fixture-hash", Role: service.RoleAdmin, Status: service.StatusActive, Concurrency: 1}
	require.NoError(t, users.Create(context.Background(), owner))
	settings := service.NewSettingService(nil, cfg)
	settings.SetScopedAdminKeyRepository(repository.NewScopedAdminKeyRepository(db))
	key, _, err := settings.CreateScopedAdminKey(context.Background(), service.ScopedAdminKeyInput{Name: "fixture", Enabled: true, Policy: principal.Policy}, owner.ID)
	require.NoError(t, err)
	_, err = settings.AuthenticateScopedAdminKey(context.Background(), key, "192.0.2.1")
	require.NoError(t, err)
	userService := service.NewUserService(users, nil, nil, nil)
	loadedOwner, err := userService.GetByID(context.Background(), owner.ID)
	require.NoError(t, err)
	require.True(t, loadedOwner.IsActive())
	require.True(t, loadedOwner.IsAdmin())
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(nil, userService, settings, nil)))
	router.Use(h.ScopedAdminKeyAccounts)
	router.GET("/api/v1/admin/accounts/import/codex-session/capabilities", h.CodexSessionImportCapabilities)
	router.POST(pausedCodexImportPath, h.ImportCodexSessionPaused)
	router.GET("/api/v1/admin/unknown", func(c *gin.Context) { t.Fatal("unregistered permission must be denied") })
	call := func(method, path, idempotencyKey string, body any) *httptest.ResponseRecorder {
		t.Helper()
		return pausedImportCall(t, router, method, path, idempotencyKey, body, key)
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		router.Handle(method, "/api/v1/admin/accounts/:id", func(c *gin.Context) { t.Fatal("unrestricted handler must not run") })
	}
	for _, action := range []string{"test", "refresh", "schedulable"} {
		router.POST("/api/v1/admin/accounts/:id/"+action, func(c *gin.Context) { t.Fatal("unrestricted handler must not run") })
	}
	unauthenticated := pausedImportCall(t, router, "GET", "/api/v1/admin/accounts/import/codex-session/capabilities", "", nil)
	require.Equal(t, 401, unauthenticated.Code)
	require.Equal(t, 403, call("GET", "/api/v1/admin/unknown", "", nil).Code)
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repository.NewIdempotencyRepository(client, db), service.DefaultIdempotencyConfig()))
	// Existing account with the same email is deliberately active in the same group.
	old := &service.Account{Name: "existing-fixture", Platform: "openai", Type: "oauth", Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"email": "paused@example.test", "access_token": "old-fixture-access"}, Extra: map[string]any{"fixture": "preserved"}, Concurrency: 1}
	require.NoError(t, accounts.Create(context.Background(), old))
	require.NoError(t, accounts.BindGroups(context.Background(), old.ID, []int64{group.ID}))
	var oldBefore string
	require.NoError(t, db.QueryRow("SELECT row_to_json(accounts)::text FROM accounts WHERE id=$1", old.ID).Scan(&oldBefore))
	// Record INSERT state in PostgreSQL itself, before any possible follow-up UPDATE.
	_, err = db.Exec(`CREATE TABLE observed_initial_account(id bigint, status text, schedulable boolean);
CREATE FUNCTION capture_paused_initial_row() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.name = 'paused-fixture' THEN
  IF NEW.status <> 'inactive' OR NEW.schedulable THEN RAISE EXCEPTION 'unsafe initial account state'; END IF;
  INSERT INTO observed_initial_account VALUES (NEW.id,NEW.status,NEW.schedulable);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER observe_initial_account AFTER INSERT ON accounts FOR EACH ROW EXECUTE FUNCTION capture_paused_initial_row();`)
	require.NoError(t, err)
	capabilityResponse := call("GET", "/api/v1/admin/accounts/import/codex-session/capabilities", "", nil)
	require.Equal(t, 200, capabilityResponse.Code)
	var capability struct {
		Data codexImportCapabilities `json:"data"`
	}
	require.NoError(t, json.Unmarshal(capabilityResponse.Body.Bytes(), &capability))
	body := pausedImportBody(capability.Data.IdempotencyScope)
	body["group_ids"] = []int64{group.ID}
	first := call("POST", pausedCodexImportPath, "durable-first-import", body)
	require.Equal(t, 200, first.Code, first.Body.String())
	var envelope struct {
		Data CodexSessionImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &envelope))
	require.Equal(t, 1, envelope.Data.Created, first.Body.String())
	require.Len(t, envelope.Data.Items, 1)
	id := envelope.Data.Items[0].AccountID
	require.NotEqual(t, old.ID, id)
	var status string
	var schedulable bool
	require.NoError(t, db.QueryRow("SELECT status,schedulable FROM observed_initial_account WHERE id=$1", id).Scan(&status, &schedulable))
	require.Equal(t, "inactive", status)
	require.False(t, schedulable)
	// Replay after a separate status edit must not create, enable, pause or otherwise mutate the row.
	_, err = db.Exec("UPDATE accounts SET status='error',schedulable=false WHERE id=$1", id)
	require.NoError(t, err)
	replay := call("POST", pausedCodexImportPath, "durable-first-import", body)
	require.Equal(t, 200, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), replay.Body.String())
	require.NoError(t, db.QueryRow("SELECT status,schedulable FROM accounts WHERE id=$1", id).Scan(&status, &schedulable))
	require.Equal(t, service.StatusError, status)
	require.False(t, schedulable)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM accounts").Scan(&count))
	require.Equal(t, 2, count)
	var oldAfter string
	require.NoError(t, db.QueryRow("SELECT row_to_json(accounts)::text FROM accounts WHERE id=$1", old.ID).Scan(&oldAfter))
	require.JSONEq(t, oldBefore, oldAfter)
	var bound int64
	require.NoError(t, db.QueryRow("SELECT group_id FROM account_groups WHERE account_id=$1", id).Scan(&bound))
	require.Equal(t, group.ID, bound)
	path := fmt.Sprintf("/api/v1/admin/accounts/%d", id)
	assertState := func(wantStatus string, wantSchedulable bool) {
		t.Helper()
		require.NoError(t, db.QueryRow("SELECT status,schedulable FROM accounts WHERE id=$1", id).Scan(&status, &schedulable))
		require.Equal(t, wantStatus, status)
		require.Equal(t, wantSchedulable, schedulable)
		require.NoError(t, db.QueryRow("SELECT group_id FROM account_groups WHERE account_id=$1", id).Scan(&bound))
		require.Equal(t, int64(2), bound)
	}
	// Test success and failure are informational only, even on an error/paused row.
	for _, code := range []int{200, 401, 429} {
		model.status = code
		var before, after string
		require.NoError(t, db.QueryRow("SELECT row_to_json(accounts)::text FROM accounts WHERE id=$1", id).Scan(&before))
		result := call("POST", path+"/test", "", map[string]any{"model_id": "gpt-5.4"})
		require.Equal(t, 200, result.Code, result.Body.String())
		require.Contains(t, result.Body.String(), fmt.Sprintf(`"success":%t`, code == 200))
		require.NotContains(t, result.Body.String(), "PRIVATE_FIXTURE")
		require.NoError(t, db.QueryRow("SELECT row_to_json(accounts)::text FROM accounts WHERE id=$1", id).Scan(&after))
		require.JSONEq(t, before, after)
	}
	require.Equal(t, 3, model.calls)
	refreshed := call("POST", path+"/refresh", "", nil)
	require.Equal(t, 200, refreshed.Code, refreshed.Body.String())
	require.Equal(t, 1, oauth.calls)
	var credential string
	require.NoError(t, db.QueryRow("SELECT credentials->>'access_token' FROM accounts WHERE id=$1", id).Scan(&credential))
	require.Equal(t, "refreshed-fixture-access", credential)
	require.NotContains(t, refreshed.Body.String(), "refreshed-fixture")
	assertState(service.StatusError, false)
	for _, update := range []map[string]any{
		{"credentials": map[string]any{"access_token": "replacement-fixture-access", "refresh_token": "replacement-fixture-refresh"}},
		{"concurrency": 4, "priority": 60},
	} {
		result := call("PUT", path, "", update)
		require.Equal(t, 200, result.Code, result.Body.String())
		assertState(service.StatusError, false)
	}
	require.NoError(t, db.QueryRow("SELECT credentials->>'access_token' FROM accounts WHERE id=$1", id).Scan(&credential))
	require.Equal(t, "replacement-fixture-access", credential)
	denied := call("PUT", path, "", map[string]any{"group_ids": []int64{2}})
	require.Equal(t, 403, denied.Code, "final policy intentionally has no accounts.groups")
	for _, active := range []bool{true, false} {
		wantStatus := "inactive"
		if active {
			wantStatus = service.StatusActive
		}
		result := call("PUT", path, "", map[string]any{"status": wantStatus, "schedulable": active})
		require.Equal(t, 200, result.Code, result.Body.String())
		assertState(wantStatus, active)
	}
	read := call("GET", path, "", nil)
	require.Equal(t, 200, read.Code, read.Body.String())
	require.NotContains(t, read.Body.String(), "replacement-fixture")
	deleted := call("DELETE", path, "", nil)
	require.Equal(t, 200, deleted.Code, deleted.Body.String())
	var isDeleted bool
	require.NoError(t, db.QueryRow("SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1", id).Scan(&isDeleted))
	require.True(t, isDeleted)
	read = call("GET", path, "", nil)
	require.Equal(t, 404, read.Code)
	// Sharing a group is not ownership. Old accounts remain in key scope, but this
	// lifecycle never targets them; the gateway must keep its exact durable ID.
	read = call("GET", fmt.Sprintf("/api/v1/admin/accounts/%d", old.ID), "", nil)
	require.Equal(t, 200, read.Code)
	require.NoError(t, db.QueryRow("SELECT row_to_json(accounts)::text FROM accounts WHERE id=$1", old.ID).Scan(&oldAfter))
	require.JSONEq(t, oldBefore, oldAfter)
}
