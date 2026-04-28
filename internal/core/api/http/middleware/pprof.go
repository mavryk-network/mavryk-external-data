package middleware

import (
	"net/http/pprof"

	"github.com/gin-gonic/gin"
)

// RegisterPprof attaches net/http/pprof under /debug/pprof/* on the given engine.
// Off by default; opt-in via cfg.Server.PprofEnabled. Bind to a private interface
// in production — pprof can disclose stack traces and code paths.
func RegisterPprof(engine *gin.Engine) {
	g := engine.Group("/debug/pprof")
	g.GET("/", gin.WrapF(pprof.Index))
	g.GET("/cmdline", gin.WrapF(pprof.Cmdline))
	g.GET("/profile", gin.WrapF(pprof.Profile))
	g.POST("/symbol", gin.WrapF(pprof.Symbol))
	g.GET("/symbol", gin.WrapF(pprof.Symbol))
	g.GET("/trace", gin.WrapF(pprof.Trace))
	for _, name := range []string{"goroutine", "heap", "threadcreate", "block", "mutex", "allocs"} {
		g.GET("/"+name, gin.WrapH(pprof.Handler(name)))
	}
}
