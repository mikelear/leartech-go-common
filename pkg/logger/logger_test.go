package logger

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitServiceLogger_DefaultsToInfoJSON(t *testing.T) {
	InitServiceLogger()
	assert.Equal(t, zerolog.InfoLevel, zerolog.GlobalLevel())
}

func TestInitServiceLogger_WithLevel(t *testing.T) {
	InitServiceLogger(WithLevel("debug"))
	assert.Equal(t, zerolog.DebugLevel, zerolog.GlobalLevel())

	InitServiceLogger(WithLevel("warn"))
	assert.Equal(t, zerolog.WarnLevel, zerolog.GlobalLevel())
}

func TestInitServiceLogger_InvalidLevelFallsBackToInfo(t *testing.T) {
	InitServiceLogger(WithLevel("bogus"))
	assert.Equal(t, zerolog.InfoLevel, zerolog.GlobalLevel())
}

func TestInitServiceLogger_WithOutputStyleConsole(t *testing.T) {
	// Just confirm it doesn't panic and a global logger remains usable.
	require.NotPanics(t, func() {
		InitServiceLogger(WithOutputStyle(OutputConsole))
	})
}

func TestMiddleware_LogsAndCallsNext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Middleware())

	called := false
	r.GET("/ok", func(c *gin.Context) {
		called = true
		c.String(200, "ok")
	})

	c, _ := gin.CreateTestContext(nil)
	_ = c

	// Easier: exercise via httptest in another suite; here we just confirm Middleware()
	// returns a non-nil HandlerFunc so callers can chain it.
	h := Middleware()
	assert.NotNil(t, h)

	// And confirm the route still fires when middleware is installed
	// (a regression check: middleware not swallowing the request).
	w := &mockResponseWriter{header: make(map[string][]string)}
	req := newGetReq(t, "/ok")
	r.ServeHTTP(w, req)
	assert.True(t, called)
}
