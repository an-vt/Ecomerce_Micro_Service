// Package openapi embeds the REST contract of the gateway (docs/05-api-design.md §1.4). A test checks that every
// route the router registers is described here, so the spec cannot drift from the code.
package openapi

import _ "embed"

// Spec is the OpenAPI 3 document served at /docs/openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
