# Native build and upstream updates

Build on a workstation or CI runner with Node.js 24, pnpm 9, Go matching
`backend/go.mod` (currently 1.27.4), Python 3, Git, and tar. No container runtime is
required to build or run the application. Backend integration tests use the
upstream test infrastructure and may require its container runtime.

From a clean checkout of the reviewed custom release commit:

```sh
VERSION=0.2.21-aibj.1 OUTPUT_DIR=/absolute/path/to/artifacts ./deploy/native/build.sh
```

`TARGET_ARCH` defaults to `amd64`; `arm64` is also accepted. If pnpm 9 is not on
PATH, the script uses Corepack with pnpm 9.15.9. Go can select the exact toolchain
through `GOTOOLCHAIN=go1.27.4`. Use a matching Node.js installation on PATH.

The archive contains `sub2api`, `resources/`, `LICENSE`, and `manifest.json`.
The manifest records the explicit custom version, source commit, build time,
target, toolchain, and file SHA256 values. The archive has a separate `.sha256`
file and `checksums.txt`. The frontend is embedded in the executable. The build
does not publish, restart services, or modify databases.

Use `ALLOW_DIRTY=1` only for local validation; the manifest records
`dirty_source: true`. The source tree must remain unchanged during compilation.
Release builds require a clean checkout. Always supply VERSION: upstream tags
can contain an older `backend/cmd/server/VERSION` value.

## Runtime configuration

- `REDEEM_PURCHASE_URL`: optional public HTTPS URL for the ordinary purchase
  link on the redeem page. Empty or invalid values hide the link. This is not
  an iframe; no login token or user identifier is added to the destination.
- `ALLOW_OFFICIAL_BINARY_UPDATES`: keep unset or `false` for this deployment.
  Official update and rollback execution are denied by default, including
  direct API calls. Only the exact value `true` opts into replacing the custom
  program with upstream binaries. Version checking remains available.

The archive is built with `BuildType=source`. The runtime update guard protects
the backend separately from the UI's source-build indicator.

## GitHub Actions

`native-checks.yml` compiles the Linux executable and runs the scoped account,
update protection, public-settings, and purchase-link regressions on PRs. Its
manual dispatch accepts a review-branch ref when GitHub suppresses generated PR
events. It has read-only repository permissions and never publishes.

`native-release.yml` builds tags such as `native-v0.2.21-aibj.1`, runs frontend and
backend checks, validates the archive, and publishes native release files only.
Its manual dispatch defaults to artifact-only validation (`publish: false`).
It does not deploy production. The upstream image release workflow is restricted
to the upstream repository so tags cannot accidentally publish that workflow's
artifacts from this fork.

`upstream-stable-sync.yml` checks the latest stable upstream release and opens a
draft merge PR. Conflicts stop the workflow before pushing; existing review
branches are left intact. Nothing merges or deploys automatically. Configure
GitHub to allow Actions to create pull requests. An optional
`UPSTREAM_SYNC_TOKEN` with repository contents and pull-request permissions
allows generated PRs to trigger normal CI. With the default `GITHUB_TOKEN`,
GitHub can suppress those triggers; validation must then be run manually before
merge. Require the frontend/backend checks and account-integration contract
checks in branch protection.

After review, release using a new `native-v...` tag. Retain custom runtime guards,
native deployment files, and account integration contracts during upstream
merges. Database migration compatibility must be validated against an isolated
restore before production deployment; switching the executable alone does not
undo migrations.
