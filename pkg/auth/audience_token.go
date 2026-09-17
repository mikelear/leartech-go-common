package auth

// PER-CALLEE TOKENS (RFC 8707).
//
// Config.TargetAudience is a single value, so a ServiceClient built from it can
// address exactly ONE callee. That is right for most services — they call one
// peer — and wrong for anything that fans out.
//
// Measured 2026-09-12: leartech-maestro-service delivers events to every
// registered consumer through one auth.TokenGetter, and had no TargetAudience
// configured at all. So its delivery tokens carried no `aud`, and a consumer
// therefore CANNOT enforce its own audience: it must accept any audience (or  proven-by: TestGetAuthTokenForAudience_RequestsTheAudienceGiven
// none) or delivery stops. Which means any service holding any valid platform
// token can post events into any consumer's /consume endpoint. That is not a
// misconfiguration, it is the shape of the contract while one token has to
// serve N callees.
//
// The Config comment has said `§A-full will add a per-call audience for
// addressing multiple callees` since the audience work began. This is that.
//
// WHAT THIS DOES AND DOES NOT CHANGE
//
// It makes per-callee tokens POSSIBLE. It does not make any consumer
// audience-enforcing — that is a per-consumer decision, and per RFC 8707's
// allow-list semantics it must go mint-side first: the caller's Hydra client  proven-by: TestGetAuthTokenForAudience_EmptyAudienceIsRefused
// needs the callee's audience in its allow-list BEFORE the callee starts
// enforcing, or delivery stops the moment enforcement lands.
//
// GetAuthToken, SetAuthHeader and HTTPClient are unchanged and still use
// cfg.TargetAudience, so nothing that exists today behaves differently.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
)

// AudienceTokenGetter mints tokens addressed to a NAMED callee.
//
// It is a separate interface rather than methods added to TokenGetter on
// purpose: TokenGetter and ServiceAuthClient have generated mocks and external
// implementers, and widening them would break every one. A caller that needs
// per-callee tokens asks for this narrower thing.
type AudienceTokenGetter interface {
	// GetAuthTokenForAudience returns a token whose `aud` is the given
	// audience, refreshing if expired. Tokens are cached per audience.
	GetAuthTokenForAudience(ctx context.Context, audience string) (*string, error)

	// SetAuthHeaderForAudience attaches such a token to req.
	SetAuthHeaderForAudience(ctx context.Context, req *http.Request, audience string) error
}

// ErrEmptyAudience is returned when no audience is supplied.
//
// It is an error rather than a fall-back to cfg.TargetAudience, and certainly
// rather than minting without an audience. An audience-less token is the exact
// failure this package exists to remove: Hydra returns one happily, every
// audience-enforcing callee refuses it, and nothing reports why. A caller that
// does not know which callee it is addressing has a bug, and it should hear
// about it here rather than as a 401 two services away.
var ErrEmptyAudience = errors.New("auth: no audience supplied — refusing to mint an " +
	"audience-less token; pass the callee's audience, or use GetAuthToken if this " +
	"service genuinely addresses only its configured TargetAudience")

// GetAuthTokenForAudience returns a token minted for the given callee.
func (c *ServiceClient) GetAuthTokenForAudience(ctx context.Context, audience string) (*string, error) {
	ts, err := c.tokenSourceFor(audience)
	if err != nil {
		return nil, err
	}
	tok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("auth: mint token for audience %q: %w", audience, err)
	}
	_ = ctx // the source holds the context used for refreshes; see baseCtx
	return &tok.AccessToken, nil
}

// SetAuthHeaderForAudience attaches a token minted for the given callee.
func (c *ServiceClient) SetAuthHeaderForAudience(ctx context.Context, req *http.Request, audience string) error {
	if req == nil {
		return errors.New("auth: nil request")
	}
	tok, err := c.GetAuthTokenForAudience(ctx, audience)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+*tok)
	return nil
}

// tokenSourceFor returns the cached oauth2.TokenSource for an audience,
// creating it on first use.
//
// One source PER AUDIENCE, because oauth2's ReuseTokenSource caches the token
// it holds — sharing a source across audiences would hand out a token minted
// for the wrong callee, which is precisely the bug being fixed and would be
// invisible until a callee started enforcing.
func (c *ServiceClient) tokenSourceFor(audience string) (oauth2.TokenSource, error) {
	if audience == "" {
		return nil, ErrEmptyAudience
	}

	c.audMu.Lock()
	defer c.audMu.Unlock()

	if c.audSources == nil {
		c.audSources = map[string]oauth2.TokenSource{}
	}
	if ts, ok := c.audSources[audience]; ok {
		return ts, nil
	}

	cfg := c.oauthCfg // copy; do not mutate the shared base
	cfg.EndpointParams = map[string][]string{"audience": {audience}}

	baseCtx := c.baseCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ts := cfg.TokenSource(baseCtx)
	c.audSources[audience] = ts
	return ts, nil
}
