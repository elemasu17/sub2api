# Scoped OpenAI account integration

The independent account manager uses a dedicated scoped admin API key in the
`x-api-key` header. Keep the key and returned credentials on the server. This
extension does not expose a browser token or require a full-access admin key.

## Active first import with scheduling disabled (v2)

The C2 import path now creates an account with `status: "active"` and
`schedulable: false` in its first database INSERT. It keeps the same group,
creation transaction, scoped permissions and response format as v1. Ordinary
account creation and the v1 `/paused` route retain their existing behavior.
No migration or scoped-key policy expansion is required. The new route maps
only to `accounts.create`; credential, schedule, optional network/model, and
OpenAI/OAuth/group restrictions are rechecked by the existing scoped service.

The capabilities response retains every v1 field below and adds:

```json
{
  "active_unscheduled": {
    "contract_version": 2,
    "import_path": "/api/v1/admin/accounts/import/codex-session/unscheduled",
    "status": "active",
    "schedulable": false,
    "idempotency_scope": "<64 lowercase hex characters>",
    "idempotency_ttl_seconds": 86400
  }
}
```

POST only to the advertised v2 path with `create_unscheduled:true`,
`contract_version:2`, `update_existing:false`, its own `idempotency_scope`, one
positive group ID, one session and a persistent `Idempotency-Key`. Conflicting
`create_paused`, v1 scope/version, missing scope/key, batches and legacy-route
fallback are rejected. The v2 scope uses an independent namespace bound to the
same key, policy, TTL and server secret. The new request flag is omitted when
false so normalized v1 requests keep their durable idempotency fingerprints.
Neither importer sends a creation-time privacy request or enables scheduling.

An optional top-level `notes` string is saved on first creation and participates
in the existing normalized idempotency fingerprint. C2 supplies only
`原始订单号：<persisted primary order tradeNo>` when available, with a trade number
of at most 100 characters and no control characters. It freezes that value with
the first import intent; retries must not change it. Omitted notes retain the
existing request behavior, and mapped credential replacement does not overwrite
operator notes. Import audit logs omit the whole request body, including notes.

Deploy Sub2API first, then the new GW. Old GW keeps its v1 inactive imports on
this server; new GW rejects a server without v2 before binding its first intent.
GW binds the initial intent to the path, version, status and schedule semantics;
old or uncertain intents require reconciliation and must never receive a fresh
key to replay under v2. A mapped credential replacement first disables scheduling,
then atomically PUTs the credentials with `status:active,schedulable:false`.
Verify and refresh disable scheduling without changing account status. Explicit
pause still sets inactive; publish still enables active scheduling.

The disposable PostgreSQL validation command below exercises both versions with
real authentication, repositories and a trigger checking their initial INSERT.
It also verifies replay does not rewrite account state or touch unrelated accounts.

## Paused first import into one existing group (v1 compatibility)

Reuse the configured OpenAI OAuth group for both pending and enabled accounts.
No migration or separate staging group is needed. The external manager must
operate only on its durable local account UUID to exact Sub2API account-ID
mapping. A group-scoped key can also reach pre-existing accounts in that group;
this integration does not add ownership permissions or automatically adopt an
account by email.

Before the first import and every retry, request
`GET /api/v1/admin/accounts/import/codex-session/capabilities` with the scoped
key. It requires the existing `accounts.create` route permission and returns:

```json
{
  "create_paused": true,
  "contract_version": 1,
  "import_path": "/api/v1/admin/accounts/import/codex-session/paused",
  "idempotency_scope": "<64 lowercase hex characters>",
  "idempotency_ttl_seconds": 86400
}
```

These fields are inside the standard response envelope's `data`. The TTL is
configuration-dependent. The scope is an opaque HMAC bound to the scoped key
ID, complete permission policy and TTL, stable across restarts while the JWT
secret is unchanged. It is a retry boundary, not an authorization token.

Use only the exact advertised `/paused` path and require all these request
fields: `create_paused: true`, `contract_version: 1`,
`update_existing: false`, `idempotency_scope` from the capability response,
one positive ID in `group_ids`, and one account in the existing `content`
format. Send a persistent `Idempotency-Key` for the local first-import operation.
The route still requires the existing scoped create, credential and scheduling
permissions (plus network/model permissions for their optional fields).

The first database INSERT has `status: "inactive"` and `schedulable: false`;
account creation and the group association commit together. There is no active
create-then-pause interval, and no automatic creation-time privacy request.
The response retains the existing import result shape with `items[].account_id`.
This create-only flow does not query or overwrite an existing account by email.
A successful idempotency replay returns the previous result without creating,
enabling, pausing or otherwise changing the saved account.

Persist the first capability scope, initial attempt time, request payload and
operation key before POST. Retries must use that same payload and key, and stop
before the original retention window ends. A key/policy/TTL/secret change, a
missing previous scope or expiry requires reconciliation. Any ambiguous POST
result (including a lost response, interrupted operation, or generic failure
that cannot prove rollback) also stops automatic retries and new import jobs
until reconciliation. Never renew the deadline or substitute a new
scope/key to make an uncertain create execute again. Response persistence and
the account transaction are separate, so an interrupted request can require
manual reconciliation even inside the TTL; this is not an exactly-once claim.

Fail closed on missing/invalid capabilities or any scope mismatch. Never fall
back to the legacy import path: older versions can ignore unknown JSON fields.
The separate POST path also rejects a downgrade between discovery and import
with an unknown-route error, before any account is created. It requires a scoped
key and a configured idempotency coordinator; interactive/full-access callers
continue using their existing import flow.

After persisting the returned ID, the manager can test the paused account with
the scoped connectivity route below, then explicitly enable it through existing
status/scheduling permissions. Group membership stays unchanged throughout.

Validation uses synthetic credentials and a disposable PostgreSQL cluster:
`python3 deploy/native/test-paused-import.py --pg-bin /path/to/postgresql/bin`.
The test follows HTTP, real admin-key authentication/route permission checks,
the actual account service and production repositories,
with a database INSERT trigger recording and rejecting an unsafe initial state.
It uses precisely `accounts.read`, `accounts.create`, `accounts.credentials`,
`accounts.schedule`, `accounts.status`, `accounts.refresh`, `accounts.delete`,
and `accounts.test`, plus global `openai.oauth.authorize`, restricted to group 2,
OpenAI OAuth, with neither all groups nor ungrouped access. It verifies exact-ID
replay, credential replacement/refresh, enable/pause/delete, rejected group
reassignment, unchanged existing email accounts and unchanged group membership.
Only upstream OAuth/model transports are synthetic. Model test success, 401 and
429 each leave the entire account row unchanged. Set `SCOPED_ADMIN_POLICY_PATH`
to an operator policy-state JSON file to assert that its `after` policy matches
this tested policy exactly; the test logs its normalized SHA256, never a key.
The fixture uses the generated current Ent schema plus the existing raw-SQL
components needed for authentication, outbox, idempotency and account deletion;
it does not certify replay of all historical migrations. The native CI and
release workflows run this check.

## OAuth authorization

Grant global permission `openai.oauth.authorize` for exactly these routes:

- `POST /api/v1/admin/openai/generate-auth-url`: body `{}` or
  `{"redirect_uri":"http://127.0.0.1:1455/auth/callback"}`. The standard response
  envelope's `data` contains `auth_url`, `session_id`, and `state`.
- `POST /api/v1/admin/openai/exchange-code`: body contains `session_id`, `code`,
  and the exact `state`. The `data` response uses the existing OpenAI token
  shape (`access_token`, `refresh_token`, `expires_at`, `expires_in`, optional
  `id_token`, identity and plan fields). Encrypt credentials before storage.

Only `http://localhost:1455/auth/callback` and
`http://127.0.0.1:1455/auth/callback` are accepted for scoped callers. The default
is the localhost form. Proxy overrides are rejected. Exchange cannot change the
stored callback. Sessions are bound to key ID and the complete permission-policy
hash; a changed policy, another key, or an interactive admin session cannot
consume them. Authentication reloads the key policy on each HTTP request.

Sessions expire after the upstream 30-minute TTL and are in memory. An accepted
key-authenticated exchange consumes its session before contacting the provider,
including if that request later fails. Start a new flow after expiry, restart,
policy changes, or ambiguous exchange failure. This prevents replay. Browser
login, consent, MFA, and challenges still occur in the provider's normal flow.

## Connectivity test

Grant account permission `accounts.test` with explicit OpenAI platform, OAuth
type, and the intended group scope. It only grants
`POST /api/v1/admin/accounts/:id/test`; it does not grant recovery, status changes,
credential reads, or access to another group.

Body: `{}` or `{"model_id":"gpt-5.4"}`. Only a text-model connectivity probe is
supported. Prompts, media, compact mode, shadow accounts, Agent Identity, and
synthetic accounts are excluded. The account scope is checked again by the test
service after loading the resource. The request is bounded to 90 seconds and
can incur normal upstream inference usage.

After an authorized test starts, the response is SSE containing only:

```text
data: {"type":"test_complete","success":true}
```

or:

```text
data: {"type":"test_complete","success":false,"code":"ACCOUNT_TEST_FAILED"}
```

An HTTP 401 from the native OpenAI text probe with a structured
`error.code` of `token_invalidated` or `token_revoked` adds a fixed field to
the failure event. The native transport sends the account's own OAuth bearer
and account identifier to the configured endpoint with redirects disabled.

```text
data: {"type":"test_complete","success":false,"code":"ACCOUNT_TEST_FAILED","error_code":"upstream_credential_invalid"}
```

This field describes the access credential tested. A caller with
`accounts.refresh` can submit one `POST /api/v1/admin/accounts/:id/refresh`.
An explicit denial from the OAuth token endpoint (`invalid_grant`,
`refresh_token_invalidated`, or `refresh_token_reused` in an HTTP 400/401)
returns HTTP 502 with JSON `code: 502` and `reason: "upstream_refresh_grant_invalid"`.
After a successful refresh, repeat the connectivity test. Refresh may also
succeed for access-only accounts by updating metadata.

Older releases and ambiguous failures omit these markers. Treat their result
as unknown. Permission errors, timeouts, rate limits and diagnostic text alone
carry no credential classification.

No model output, raw provider error, account token, or identity is returned.
Scoped tests do not recover runtime state or persist error, rate-limit, or
quota-snapshot changes. Invalid/unauthorized requests fail before probing with
ordinary HTTP errors; callers must not interpret HTTP 200 alone as test success.

Existing scoped permissions separately control account creation, credential
updates, refresh, status, groups, and deletion. Keep a durable local mapping from
the source order/account to the Sub2API account ID, and reconcile before retrying
ambiguous create/delete operations. Do not put passwords or tokens in names,
notes, URLs, audit messages, or public DTOs.

Before updating upstream, run the scoped OAuth/session and account-test
regressions, plus the external manager's HTTP contract tests. The first rollout
must leave external feature flags disabled until these capabilities are deployed
and verified using an isolated test account.

## Durable account cost for an external scheduler

The C2/GW scheduler reads the account-billed dollar total from
`GET /api/v1/admin/accounts/:id/lifetime-usage`. This endpoint is read-only and
accepts either `accounts.stats.read` or `accounts.usage.read` for the same
scoped account. It returns `total_account_cost` and `updated_at` in the normal
response envelope. The value is calculated from the account-billed cost and
the rate multiplier captured on each usage log.

Apply `migrations/250_account_lifetime_usage.sql` before enabling the GW quota
cron. The migration backfills existing usage logs and installs an
`AFTER INSERT` trigger, so later usage-log retention does not reduce the total.
Sub2API applies this file through its normal PostgreSQL startup migration runner,
which records the checksum in `schema_migrations`. Do not execute the SQL manually
against a running deployment or insert a ledger row by hand. When PostgreSQL is
unavailable, the migration is pending and the GW quota scheduler must remain
disabled until a service restart completes the runner and the table, trigger, and
ledger row are verified.
The GW records a post-import baseline and accumulates only positive increases;
an observed total decrease (for example after a legacy reset) is never treated
as negative usage. During a staged rollout, a GW that receives 404 from this
route falls back to the existing rolling statistics endpoint, but that fallback
does not provide the same retention guarantee and should be removed after all
Sub2API replicas have applied migration 250.

The C2/GW policy treats a post-import cumulative amount above `$90` as a
permanent automatic-stop condition. This is an observation-based soft stop:
the scheduler stops accepting new work after the next cron pass while
in-flight requests may finish and be billed. This revision has no reset endpoint;
restarting Sub2API or deleting old usage logs must not reset it. Any future
reset requires an explicit, reviewed operator action with an audit record.
