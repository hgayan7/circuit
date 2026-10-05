#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
out=${1:?Usage: sh scripts/package-sdks.sh NEW_OUTPUT_DIRECTORY}
if [ -e "$out" ]; then echo 'Output directory must not exist' >&2; exit 1; fi
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
npm ci --prefix sdk/typescript
npm pack ./sdk/typescript --pack-destination "$out"
python3 -m venv "$work/venv"
"$work/venv/bin/pip" install 'build==1.2.2'
"$work/venv/bin/python" -m build --outdir "$out" sdk/python
tar -czf "$out/circuit-go-sdk-0.2.0.tar.gz" -C sdk/go .
cp api/openapi.yaml "$out/circuit-openapi-1.0.0.yaml"
echo "SDK release artifacts: $out"
