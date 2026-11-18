#!/usr/bin/env bash
# Regenerates every file derived from api/openapi.yaml, the migrations and queries, and the Figma tokens.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
export GOTOOLCHAIN=local GOFLAGS=-mod=readonly

if [[ -n "${BENCHWATCH_NODE_BIN:-}" ]]; then
  node="$BENCHWATCH_NODE_BIN/node"
else
  node="$(command -v node || true)"
fi
if [[ -z "$node" || ! -x "$node" ]]; then
  echo "generate: node not found; put Node 26 on PATH or set BENCHWATCH_NODE_BIN to its bin folder" >&2
  exit 1
fi
major="$("$node" -p 'process.versions.node.split(".")[0]')"
if (( major < 26 )); then
  echo "generate: Node $major found at $node; Node 26 or later is required (web/package.json engines)" >&2
  exit 1
fi

"$node" "$root/bin/check-node-modules.mjs" "$root/web/codegen"

cd "$root/server"
go tool -modfile=tools.mod oapi-codegen -config internal/api/oapi-codegen.yaml ../api/openapi.yaml
go tool -modfile=tools.mod sqlc generate
gofmt -l internal | { ! grep .; }

cd "$root/web"
"$node" codegen/node_modules/openapi-typescript/bin/cli.js ../api/openapi.yaml -o src/api/schema.d.ts
"$node" "$root/bin/tokens.mjs" "$root/design/figma-tokens.json" src/tokens/tokens.css
"$node" "$root/bin/tokens.mjs" "$root/design/figma-tokens.json" src/tokens/tokens.css --check

echo "generated: server/internal/api, server/internal/store, web/src/api/schema.d.ts, web/src/tokens/tokens.css"
