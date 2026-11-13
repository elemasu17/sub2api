# Scoped OpenAI account integration

The independent account manager uses a dedicated scoped admin API key in the
`x-api-key` header. Keep the key and returned credentials on the server. This
extension does not expose a browser token or require a full-access admin key.

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
