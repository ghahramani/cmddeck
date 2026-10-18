#!/usr/bin/env bash
set -euo pipefail

tag=${1:?Usage: bash scripts/build-release.sh v2.1.0}
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "Expected a version tag such as v2.1.0, got: $tag" >&2
  exit 1
fi
version=${tag#v}
project=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
workspace=$(mktemp -d)
trap 'rm -r -- "$workspace"' EXIT
cd "$project"
mkdir -p dist

archives=()
for architecture in amd64 arm64; do
  folder="$workspace/$architecture"
  mkdir -p "$folder"
  CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
    go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=$version" \
      -o "$folder/cmddeck" .
  name="cmddeck_${version}_linux_${architecture}.tar.gz"
  tar -czf "dist/$name" -C "$folder" cmddeck -C "$project" README.md config.example.json
  archives+=("$name")
done

cd dist
sha256sum "${archives[@]}" > SHA256SUMS
echo "Release artifacts are ready in $project/dist"
