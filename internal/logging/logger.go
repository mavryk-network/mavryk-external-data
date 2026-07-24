package logging

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const defaultRequestIDHeader = "X-Request-ID"

// maxUserAgentLen caps the logged User-Agent so an attacker cannot force
// multi-KiB lines into every log entry (log-storage amplification).
const maxUserAgentLen = 256

// requestIDPattern bounds a client-supplied X-Request-ID: a short, safe token.
// Anything else (oversized headers, control chars, forged job-style ids) is
// rejected and replaced with a server-generated uuid.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// sensitiveQueryParams are stripped from any URL before it is logged. The
// Equiteez bypass secret rides in `?bypass=`, and other integrations may pass
// keys/tokens the same way.
var sensitiveQueryParams = map[string]struct{}{
	"bypass": {}, "key": {}, "token": {}, "secret": {},
	"password": {}, "api_key": {}, "apikey": {}, "access_token": {},
}

type contextKey string

const requestIDKey contextKey = "request_id"

// NewLogger builds the root logger from LOG_LEVEL env (default: info) and
// emits one info line at startup recording the active level + caller
// annotation status — see refactoring_v2 §6.2.
func NewLogger() *zerolog.Logger {
	level := zerolog.InfoLevel
	var levelWarn string
	if val := os.Getenv("LOG_LEVEL"); val != "" {
		if parsed, err := zerolog.ParseLevel(strings.ToLower(val)); err == nil {
			level = parsed
		} else {
			levelWarn = val // rejected — surfaced below instead of silently ignored
		}
	}

	withCaller := true
	var callerWarn string
	if val := os.Getenv("LOG_CALLER"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			withCaller = b
		} else {
			callerWarn = val // only the literal "false" used to disable — now any bool form works
		}
	}

	ctx := zerolog.New(os.Stdout).
		Level(level).
		With().
		Timestamp()
	if withCaller {
		ctx = ctx.Caller()
	}
	z := ctx.Logger()

	if levelWarn != "" {
		z.Warn().Str("log_level", levelWarn).Msg("invalid LOG_LEVEL ignored; using info")
	}
	if callerWarn != "" {
		z.Warn().Str("log_caller", callerWarn).Msg("invalid LOG_CALLER ignored; using true")
	}

	z.Info().
		Str("level", level.String()).
		Bool("caller_annotated", withCaller).
		Msg("logging_initialized")

	return &z
}

func WithComponent(base *zerolog.Logger, component string) *zerolog.Logger {
	if base == nil {
		return nil
	}
	child := base.With().Str("component", component).Logger()
	return &child
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val, ok := ctx.Value(requestIDKey).(string); ok {
		return val
	}
	return ""
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, requestIDKey, requestID)
}

func WithLogger(ctx context.Context, logger *zerolog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return logger.WithContext(ctx)
}

func LoggerFromContext(ctx context.Context, fallback *zerolog.Logger) *zerolog.Logger {
	if ctx != nil {
		if ctxLogger := zerolog.Ctx(ctx); ctxLogger != nil && ctxLogger.GetLevel() != zerolog.NoLevel {
			level := ctxLogger.GetLevel()
			if level != zerolog.NoLevel && level != zerolog.Disabled {
				return ctxLogger
			}
		}
	}
	if fallback != nil {
		return fallback
	}
	disabled := zerolog.Nop()
	return &disabled
}

func RequestIDMiddleware(headerName string) gin.HandlerFunc {
	if headerName == "" {
		headerName = defaultRequestIDHeader
	}

	return func(c *gin.Context) {
		requestID := sanitizeRequestID(c.GetHeader(headerName))
		if requestID == "" {
			requestID = uuid.NewString()
		}

		c.Writer.Header().Set(headerName, requestID)
		ctx := WithRequestID(c.Request.Context(), requestID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

func RequestLogger(base *zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := RequestIDFromContext(c.Request.Context())
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		requestLogger := LoggerFromContext(c.Request.Context(), base).With().
			Str("request_id", requestID).
			Str("method", c.Request.Method).
			Str("path", path).
			Str("client_ip", c.ClientIP()).
			// Also log the raw direct peer: client_ip honors X-Forwarded-For and
			// (even with trusted-proxies locked down) can be shaped by the edge, so
			// remote_addr preserves the true source for incident forensics.
			Str("remote_addr", c.Request.RemoteAddr).
			Str("user_agent", truncate(c.Request.UserAgent(), maxUserAgentLen)).
			Logger()

		ctx := WithLogger(c.Request.Context(), &requestLogger)
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		bytesOut := c.Writer.Size()

		event := requestLogger.Info().
			Int("status", status).
			Dur("latency", latency).
			Int("bytes_out", bytesOut)

		if len(c.Errors) > 0 {
			event.Str("errors", c.Errors.String())
		}

		event.Msg("http_request_completed")
	}
}

type HTTPTransport struct {
	Base      http.RoundTripper
	Logger    *zerolog.Logger
	Component string
}

func (t *HTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	logger := LoggerFromContext(req.Context(), WithComponent(t.Logger, t.Component))
	start := time.Now()
	rid := RequestIDFromContext(req.Context())

	safeURL := redactURL(req.URL)

	resp, err := base.RoundTrip(req)
	if err != nil {
		// Scrub the raw URL out of the error too: transport errors are
		// *url.Error values whose Error() embeds the full URL (secret and all)
		// and propagate into caller/job logs. scrubErrorURL keeps errors.Is/As
		// working via Unwrap while the message shows the redacted URL.
		err = scrubErrorURL(err, req.URL, safeURL)
		ev := logger.Error().
			Err(err).
			Str("method", req.Method).
			Str("url", safeURL).
			Dur("latency", time.Since(start))
		if rid != "" {
			ev = ev.Str("request_id", rid)
		}
		ev.Msg("http_request_failed")
		return nil, err
	}

	ev := logger.Info().
		Int("status", resp.StatusCode).
		Str("method", req.Method).
		Str("url", safeURL).
		Dur("latency", time.Since(start))
	if rid != "" {
		ev = ev.Str("request_id", rid)
	}
	ev.Msg("http_request_sent")

	return resp, nil
}

// sanitizeRequestID returns a trimmed client-supplied request id when it matches
// the tight allow-pattern, otherwise "" so the caller mints a fresh uuid. Rejects
// oversized, control-char, and forged ids before they reach logs/response headers.
func sanitizeRequestID(v string) string {
	v = strings.TrimSpace(v)
	if requestIDPattern.MatchString(v) {
		return v
	}
	return ""
}

// truncate caps s to n bytes (rune-safe at the boundary is not required for a
// log field; we just bound size), appending an ellipsis marker when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// redactURL returns u as a string with the values of sensitive query params
// replaced by "REDACTED". Returns u unchanged when nothing sensitive is present.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	q := u.Query()
	redacted := false
	for k := range q {
		if _, sensitive := sensitiveQueryParams[strings.ToLower(k)]; sensitive {
			q.Set(k, "REDACTED")
			redacted = true
		}
	}
	if !redacted {
		return u.String()
	}
	clone := *u
	clone.RawQuery = q.Encode()
	return clone.String()
}

// scrubErrorURL replaces every occurrence of the raw URL with its redacted form
// in an error's message, preserving the original for errors.Is/As via Unwrap.
func scrubErrorURL(err error, raw *url.URL, safe string) error {
	if err == nil || raw == nil {
		return err
	}
	rawStr := raw.String()
	if rawStr == safe || !strings.Contains(err.Error(), rawStr) {
		return err
	}
	return &scrubbedError{msg: strings.ReplaceAll(err.Error(), rawStr, safe), err: err}
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }
