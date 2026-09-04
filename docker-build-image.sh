#!/usr/bin/env bash
set -euo pipefail

VERSION=${1:?"Usage: $0 <version> [image] [build-date]"}
IMAGE=${2:-new-api}
BUILD_DATE=${3:-$(date -u +'%Y-%m-%dT%H:%M:%SZ')}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

printf '%s\n' "$VERSION" > "$ROOT/VERSION"
docker build \
  --label "org.opencontainers.image.version=$VERSION" \
  --label "org.opencontainers.image.created=$BUILD_DATE" \
  -t "$IMAGE:$VERSION" \
  "$ROOT"
