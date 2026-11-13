#!/usr/bin/env bash
# Build a native Linux release; never publishes or changes a running service.
set -euo pipefail
umask 022

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
VERSION=${VERSION:?Set VERSION explicitly, e.g. 0.2.21-aibj.1}
TARGET_ARCH=${TARGET_ARCH:-amd64}
OUTPUT_DIR=${OUTPUT_DIR:-"$ROOT/dist/native"}
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]; then
  echo 'VERSION must be a release version without a v prefix' >&2; exit 1
fi
case "$TARGET_ARCH" in amd64|arm64) ;; *) echo 'Unsupported TARGET_ARCH' >&2; exit 1 ;; esac

for tool in node go git python3 tar; do command -v "$tool" >/dev/null || { echo "Missing $tool" >&2; exit 1; }; done
[[ $(node -p 'process.versions.node.split(".")[0]') == 24 ]] || { echo 'Node.js 24 is required' >&2; exit 1; }
GO_VERSION=$(awk '/^go / {print $2; exit}' "$ROOT/backend/go.mod")
[[ $(go env GOVERSION) == "go$GO_VERSION" ]] || { echo "Go $GO_VERSION is required" >&2; exit 1; }
if command -v pnpm >/dev/null && [[ $(pnpm --version) == 9.* ]]; then
  PNPM=(pnpm)
else
  command -v corepack >/dev/null || { echo 'pnpm 9 or Corepack is required' >&2; exit 1; }
  PNPM=(corepack pnpm@9.15.9)
fi
COMMIT=$(git -C "$ROOT" rev-parse HEAD)
DIRTY=false
if [[ -n $(git -C "$ROOT" status --porcelain --untracked-files=normal) ]]; then
  DIRTY=true
  [[ ${ALLOW_DIRTY:-0} == 1 ]] || { echo 'Refusing a dirty source tree; set ALLOW_DIRTY=1 for local validation only' >&2; exit 1; }
fi
BUILD_DATE=${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR=$(cd "$OUTPUT_DIR" && pwd)
STAGING=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-native.XXXXXXXX")
trap 'rm -rf "$STAGING"' EXIT

cd "$ROOT/frontend"
"${PNPM[@]}" install --frozen-lockfile
"${PNPM[@]}" run build
cd "$ROOT/backend"
CGO_ENABLED=0 GOOS=linux GOARCH="$TARGET_ARCH" go build -tags embed -trimpath \
  -ldflags="-s -w -X main.Version=$VERSION -X main.Commit=$COMMIT -X main.Date=$BUILD_DATE -X main.BuildType=source" \
  -o "$STAGING/sub2api" ./cmd/server
cp -R "$ROOT/backend/resources" "$STAGING/resources"
cp "$ROOT/LICENSE" "$STAGING/LICENSE"
python3 - "$STAGING" "$VERSION" "$COMMIT" "$BUILD_DATE" "$TARGET_ARCH" "$GO_VERSION" "$DIRTY" <<'PY'
import hashlib, json, pathlib, sys
root, version, commit, date, arch, go_version, dirty = sys.argv[1:]
root = pathlib.Path(root)
files = {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
         for p in sorted(root.rglob('*')) if p.is_file()}
(root / 'manifest.json').write_text(json.dumps({
    'version': version, 'commit': commit, 'built_at': date, 'os': 'linux',
    'arch': arch, 'go_version': go_version, 'build_type': 'source',
    'dirty_source': dirty == 'true', 'files': files,
}, indent=2) + '\n')
PY
ARCHIVE="sub2api_${VERSION}_linux_${TARGET_ARCH}.tar.gz"
COPYFILE_DISABLE=1 tar -czf "$OUTPUT_DIR/$ARCHIVE" -C "$STAGING" sub2api resources LICENSE manifest.json
python3 - "$OUTPUT_DIR" "$ARCHIVE" <<'PY'
import hashlib, pathlib, sys
directory, name = pathlib.Path(sys.argv[1]), sys.argv[2]
digest = hashlib.sha256((directory / name).read_bytes()).hexdigest()
(directory / (name + '.sha256')).write_text(f'{digest}  {name}\n')
(directory / 'checksums.txt').write_text(f'{digest}  {name}\n')
print(directory / name)
print(directory / 'checksums.txt')
PY
