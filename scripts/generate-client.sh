#!/bin/sh
set -eu
if [ "$#" != 2 ]; then
  echo 'Usage: sh scripts/generate-client.sh GENERATOR NEW_OUTPUT_DIRECTORY' >&2
  echo 'Examples: java, kotlin, csharp, swift5, rust, dart, typescript-fetch, python, go' >&2
  exit 1
fi
case "$1" in *[!a-zA-Z0-9-]*|'') echo 'Invalid generator name' >&2; exit 1;; esac
if [ -e "$2" ]; then echo 'Choose a new output directory; generated clients never overwrite existing code' >&2; exit 1; fi
mkdir -p "$2"
out=$(cd "$2" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)
image='openapitools/openapi-generator-cli:v7.15.0@sha256:509f01c3c7eee9d1ad286506a7b6aa4624a95b410be9a238a306d209e900621f'
docker pull "$image"
docker run --rm --user "$(id -u):$(id -g)" --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,nosuid,size=256m -v "$root/api:/contract:ro" -v "$out:/output" "$image" \
  generate -i /contract/openapi.yaml -g "$1" -o /output \
  --additional-properties hideGenerationTimestamp=true --git-user-id hgayan7 --git-repo-id circuit
echo 'Generated bindings. Configure verified TLS, agent-only authentication, bounded timeouts and no automatic dispatch retries. Check action.state, not HTTP success.'
