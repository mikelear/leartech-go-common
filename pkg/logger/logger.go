// Package logger provides structured logging initialization and HTTP middleware.
// Replaces spring-financial-group/mqube-go-common/pkg/logger.
package logger

import (
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// OutputStyle controls log formatting.
type OutputStyle string

const (
	OutputJSON    OutputStyle = "json"
	OutputConsole OutputStyle = "console"
)

// Option configures the logger.
type Option func(*loggerConfig)

type loggerConfig struct {
	level       string
	outputStyle OutputStyle
}

// WithLevel sets the log level (debug, info, warn, error).
func WithLevel(level string) Option {
	return func(c *loggerConfig) {
		c.level = level
	}
}

// WithOutputStyle sets JSON or console output.
func WithOutputStyle(style OutputStyle) Option {
	return func(c *loggerConfig) {
		c.outputStyle = style
	}
}

// InitServiceLogger initializes the global zerolog logger.
func InitServiceLogger(opts ...Option) {
	cfg := &loggerConfig{
		level:       "info",
		outputStyle: OutputJSON,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	level, err := zerolog.ParseLevel(cfg.level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	if cfg.outputStyle == OutputConsole {
		log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
			With().Timestamp().Caller().Logger()
	} else {
		log.Logger = zerolog.New(os.Stdout).
			With().Timestamp().Logger()
	}
}

// Middleware returns a gin middleware that logs requests with zerolog.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		log.Info().
			Str("method", c.Request.Method).
			Str("path", path).
			Int("status", c.Writer.Status()).
			Dur("latency", time.Since(start)).
			Str("ip", c.ClientIP()).
			Msg("request")
	}
}
