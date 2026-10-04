// Package api publishes the language-neutral agent contract. MCP uses the same action core.
package api

import _ "embed"

//go:embed openapi.yaml
var Contract []byte
