# Scoped OpenAI account integration

The independent account manager uses a dedicated scoped admin API key in the
`x-api-key` header. Keep the key and returned credentials on the server. This
extension does not expose a browser token or require a full-access admin key.

## Paused first import into one existing group

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
