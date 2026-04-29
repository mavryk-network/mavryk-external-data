// Package docs embeds the OpenAPI 3.0 specification and the Swagger UI shell
// so the HTTP server can serve them as static assets without depending on any
// runtime spec generator (we used to use swaggo/swag — see ADR-NN). The spec
// is the source of truth; routes mounted by the HTTP layer simply hand bytes
// to the client.
package docs

import _ "embed"

//go:embed openapi.yaml
var OpenAPIYAML []byte

//go:embed index.html
var SwaggerUIHTML []byte
