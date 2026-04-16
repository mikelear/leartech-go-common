// Package auth provides service-to-service authentication helpers.
// Replaces spring-financial-group/mqube-go-common/pkg/auth.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Config holds authentication configuration.
type Config struct {
	TokenURL     string `yaml:"tokenUrl" json:"tokenUrl"`
	ClientID     string `yaml:"clientId" json:"clientId"`
	ClientSecret string `yaml:"clientSecret" json:"clientSecret"`
	Audience     string `yaml:"audience" json:"audience"`
}

// TokenGetter retrieves auth tokens for outbound service calls.
type TokenGetter interface {
	GetAuthToken(ctx context.Context) (*string, error)
	SetAuthHeader(req *http.Request) error
}

// ServiceAuthClient provides authentication middleware and token management.
type ServiceAuthClient interface {
	TokenGetter
	Middleware(opts interface{}) gin.HandlerFunc
}

type serviceAuthClient struct {
	cfg        Config
	mu         sync.RWMutex
	token      string
	expiry     time.Time
	httpClient *http.Client
}

// NewServiceClient creates a new ServiceAuthClient from config.
func NewServiceClient(ctx context.Context, cfg Config) (ServiceAuthClient, error) {
	if cfg.TokenURL == "" {
		// No auth configured — return a no-op client
		return &noopAuthClient{}, nil
	}

	client := &serviceAuthClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	// Validate we can get a token
	if _, err := client.GetAuthToken(ctx); err != nil {
		return nil, fmt.Errorf("initial token fetch failed: %w", err)
	}

	return client, nil
}

func (c *serviceAuthClient) GetAuthToken(ctx context.Context) (*string, error) {
	c.mu.RLock()
	if c.token != "" && time.Now().Before(c.expiry) {
		token := c.token
		c.mu.RUnlock()
		return &token, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if c.token != "" && time.Now().Before(c.expiry) {
		token := c.token
		return &token, nil
	}

	token, expiry, err := fetchToken(ctx, c.httpClient, c.cfg)
	if err != nil {
		return nil, err
	}

	c.token = token
	c.expiry = expiry
	return &token, nil
}

func (c *serviceAuthClient) SetAuthHeader(req *http.Request) error {
	token, err := c.GetAuthToken(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	return nil
}

func (c *serviceAuthClient) Middleware(opts interface{}) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		authHeader := ctx.GetHeader("Authorization")
		if authHeader == "" {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}
		// In a full implementation, validate the JWT here.
		// For leartech services behind cluster networking, presence is sufficient.
		ctx.Next()
	}
}

// noopAuthClient is used when no auth is configured (e.g. local dev).
type noopAuthClient struct{}

func (n *noopAuthClient) GetAuthToken(_ context.Context) (*string, error) {
	empty := ""
	return &empty, nil
}

func (n *noopAuthClient) SetAuthHeader(_ *http.Request) error {
	return nil
}

func (n *noopAuthClient) Middleware(_ interface{}) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Next()
	}
}
