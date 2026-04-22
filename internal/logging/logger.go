package logging

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const defaultRequestIDHeader = "X-Request-ID"

type contextKey string

const requestIDKey contextKey = "request_id"

func NewLogger() *zerolog.Logger {
	level := zerolog.InfoLevel
	if val := os.Getenv("LOG_LEVEL"); val != "" {
		if parsed, err := zerolog.ParseLevel(strings.ToLower(val)); err == nil {
			level = parsed
		}
	}

	z := zerolog.New(os.Stdout).
		Level(level).
		With().
		Timestamp().
		Logger()

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
		requestID := c.GetHeader(headerName)
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
			Str("user_agent", c.Request.UserAgent()).
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

	resp, err := base.RoundTrip(req)
	if err != nil {
		logger.Error().
			Err(err).
			Str("method", req.Method).
			Str("url", req.URL.String()).
			Dur("latency", time.Since(start)).
			Msg("http_request_failed")
		return nil, err
	}

	logger.Info().
		Int("status", resp.StatusCode).
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Dur("latency", time.Since(start)).
		Msg("http_request_sent")

	return resp, nil
}
