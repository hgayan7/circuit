#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
GENERATOR='openapitools/openapi-generator-cli:v7.15.0@sha256:509f01c3c7eee9d1ad286506a7b6aa4624a95b410be9a238a306d209e900621f'
generate() {
  docker run --rm --user "$(id -u):$(id -g)" --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /tmp:rw,nosuid,size=256m -v "$PWD:/local" "$GENERATOR" generate \
    -i /local/api/openapi.yaml -g "$1" -o "/local/sdk/$2" \
    --git-user-id hgayan7 --git-repo-id "${4:-circuit}" \
    --additional-properties "$3,hideGenerationTimestamp=true" \
    --global-property apiDocs=false,modelDocs=false,apiTests=false,modelTests=false
}
docker pull "$GENERATOR"
generate typescript-fetch typescript 'npmName=@circuit/agent-client,npmVersion=0.2.0'
generate python python 'packageName=circuit_client,projectName=circuit-agent-client,packageVersion=0.2.0'
generate go go 'packageName=circuitclient,packageVersion=0.2.0,isGoSubmodule=false' 'circuit/sdk/go'
# Export the handwritten facade alongside generated wire bindings.
printf '\nexport * from '\''./circuit'\'';\n' >> sdk/typescript/src/index.ts
node scripts/sdk-package.cjs
gofmt -w sdk/go/*.go
node scripts/normalize-sdk.cjs
