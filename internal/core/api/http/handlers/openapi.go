package handlers

import (
	"net/http"

	"quotes/docs"

	"github.com/gin-gonic/gin"
)

// OpenAPISpec serves the canonical OpenAPI 3.0 YAML embedded in the binary.
// The bytes are the source of truth; the file in docs/openapi.yaml is what
// `go embed` reads at compile time. Updates to the spec require a rebuild.
func OpenAPISpec() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", docs.OpenAPIYAML)
	}
}

// SwaggerUI serves the embedded Swagger UI shell that loads /openapi.yaml.
// Bind /docs (no trailing slash) and /docs/ to this handler so deep-links work.
func SwaggerUI() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", docs.SwaggerUIHTML)
	}
}
