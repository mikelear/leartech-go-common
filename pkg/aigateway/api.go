// Package gateway is the client for the ai-gateway admin API.
package aigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Key is one virtual key as the gateway reports it.
type Key struct {
	KeyID          string     `json:"keyid"`
	Name           string     `json:"name"`
	Scopes         []string   `json:"scopes"`
	ModelAllowlist []string   `json:"model_allowlist"`
	RateLimitRPM   *int       `json:"rate_limit_rpm,omitempty"`
	BudgetMicros   *int64     `json:"budget_micros,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	CreatedBy      *string    `json:"created_by,omitempty"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

// Revoked reports whether the gateway has revoked this key.
//
// proven-by: TestKey_RevokedIsDrivenByThePresenceOfRevokedAt
func (k Key) Revoked() bool { return k.RevokedAt != nil }

// DeniesEveryModel reports a key whose model_allowlist is empty.
//
// An empty list means DENY, not "no restriction". The gateway's
// filterAllowed keeps only candidates present in the list, so an empty one
// matches nothing, and migration 00013 flipped this deliberately:
//
// "An empty model_allowlist used to mean every model in the tenant catalog."
// "It now means DENY, because absence must never be the permissive branch."
// "That semantics is what let a caller with no credential reach every model."
//
// source: leartech-ai-gateway migrations/00013_model_allowlist_explicit.sql
//
// This was previously named Unrestricted and reported the opposite, so
// `keys list` printed "ALL (no allowlist)" for the `agent` key, which
// 00013 left empty on purpose and which therefore denies everything.
// Telling a reader a credential is maximally capable when it is inert is
// the worst available direction for the error.
//
// proven-by: TestKey_AnEmptyAllowlistDeniesEveryModel
func (k Key) DeniesEveryModel() bool { return len(k.ModelAllowlist) == 0 }

// ListKeysResponse is GET /admin/v1/keys.
type ListKeysResponse struct {
	Keys []Key `json:"keys"`
}

// CreateKeyRequest is POST /admin/v1/keys.
type CreateKeyRequest struct {
	Name           string   `json:"name"`
	Scopes         []string `json:"scopes"`
	ModelAllowlist []string `json:"model_allowlist"`
	RateLimitRPM   *int     `json:"rate_limit_rpm"`
	BudgetMicros   *int64   `json:"budget_micros"`
	ExpiresAt      *string  `json:"expires_at"`
}

// CreateKeyResponse carries the minted secret, which is shown once.
type CreateKeyResponse struct {
	KeyID  string `json:"keyid"`
	Secret string `json:"secret"`
	Key    Key    `json:"key"`
}

// AmendKeyRequest is PATCH /admin/v1/keys/{keyid}.
//
// Every field is a pointer with omitempty so an unset one is absent from
// the wire: the gateway overwrites whatever it receives, and a zeroed
// budget_micros would zero a live budget.
//
// proven-by: TestAmendKeyRequest_OmitsWhatWasNotAskedFor
type AmendKeyRequest struct {
	Name           *string   `json:"name,omitempty"`
	Scopes         *[]string `json:"scopes,omitempty"`
	ModelAllowlist *[]string `json:"model_allowlist,omitempty"`
	RateLimitRPM   *int      `json:"rate_limit_rpm,omitempty"`
	BudgetMicros   *int64    `json:"budget_micros,omitempty"`
	ExpiresAt      *string   `json:"expires_at,omitempty"`
}

// UsageRow is one (key, model) pair's usage. Field names come from the
// gateway's store.UsageRow.
//
// proven-by: TestUsageRow_DecodesEveryFieldTheGatewaySends
type UsageRow struct {
	KeyID            string `json:"keyid"`
	Model            string `json:"model"`
	Calls            int64  `json:"calls"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CostMicros       int64  `json:"cost_micros"`

	CacheReadTokens    int64 `json:"cache_read_tokens"`
	CacheWrite5mTokens int64 `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`

	// CacheableCalls is how many of Calls reached a supplier that reports a
	// cache at all. It is the hit-rate DENOMINATOR: without it the rate is
	// reads over all calls, which counts traffic to suppliers with no prompt
	// cache as traffic that failed to hit one.
	// proven-by: TestUsageRow_HitRateExcludesSuppliersWithNoCache
	CacheableCalls int64 `json:"cacheable_calls"`
}

// CacheWriteTokens is both tiers together.
// proven-by: TestUsageRow_HitRateExcludesSuppliersWithNoCache
func (r UsageRow) CacheWriteTokens() int64 {
	return r.CacheWrite5mTokens + r.CacheWrite1hTokens
}

// HitRate is cached tokens as a fraction of the prompt tokens that COULD have
// been cached, or -1 when the question does not apply.
//
// Returns -1 rather than 0 for a supplier with no cache: zero is a real
// answer, and no cache is not an answer. // proven-by: TestUsageRow_NoCacheableCallsHasNoHitRate
// proven-by: TestUsageRow_HitRateExcludesSuppliersWithNoCache
// proven-by: TestUsageRow_AColdCacheIsZeroNotAbsent
func (r UsageRow) HitRate() float64 {
	if r.CacheableCalls == 0 {
		return -1
	}
	total := r.PromptTokens + r.CacheReadTokens + r.CacheWriteTokens()
	if total == 0 {
		return -1
	}
	return float64(r.CacheReadTokens) / float64(total)
}

// UsageResponse is GET /admin/v1/usage.
type UsageResponse struct {
	Since time.Time  `json:"since"`
	Rows  []UsageRow `json:"rows"`
}

// ListKeys returns the tenant's virtual keys.
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	var r ListKeysResponse
	if err := c.do(ctx, http.MethodGet, "/admin/v1/keys", nil, &r); err != nil {
		return nil, err
	}
	return r.Keys, nil
}

// CreateKey mints a key. The response carries the secret; the gateway does
// not offer a way to read it again afterwards.
func (c *Client) CreateKey(ctx context.Context, req CreateKeyRequest) (CreateKeyResponse, error) {
	var r CreateKeyResponse
	if err := c.do(ctx, http.MethodPost, "/admin/v1/keys", req, &r); err != nil {
		return CreateKeyResponse{}, err
	}
	return r, nil
}

// RotateKey mints a new secret and invalidates the previous one.
func (c *Client) RotateKey(ctx context.Context, keyid string) (CreateKeyResponse, error) {
	var r CreateKeyResponse
	if err := c.do(ctx, http.MethodPost, "/admin/v1/keys/"+keyid+"/rotate", nil, &r); err != nil {
		return CreateKeyResponse{}, err
	}
	return r, nil
}

// AmendKey changes the fields present in req and leaves the rest alone.
// The amend response is a keys LIST, not a bare key. Decoding it as a bare
// key left every field at its zero value, so a successful amend printed the
// key as having no allowlist and no ceiling -- "NONE (empty list denies)" and
// "unlimited" -- for a key that had just been given a $750 ceiling and five
// models. The confirmation of a privileged change reported the inverse of the
// change.
//
// Third instance of one shape in this client: scp typed as a string against
// an array, apiError.error typed as a string against an object, and this. A
// field nobody decodes is indistinguishable from a field nobody sends, and
// the zero value that results reads as a real answer.
//
// source: leartech-ai-gateway internal/api/admin.go AmendKey -- ListKeysResponse
//
// proven-by: TestAmendKey_DecodesTheListShapeTheGatewayReturns
// proven-by: TestAmendKey_AnEmptyKeyListIsAnErrorNotAZeroKey
func (c *Client) AmendKey(ctx context.Context, keyid string, req AmendKeyRequest) (Key, error) {
	var resp ListKeysResponse
	if err := c.do(ctx, http.MethodPatch, "/admin/v1/keys/"+keyid, req, &resp); err != nil {
		return Key{}, err
	}
	if len(resp.Keys) == 0 {
		return Key{}, fmt.Errorf("the gateway reported %s amended but returned no key; "+
			"run `ship-proven keys list` to see what it now holds", keyid)
	}
	return resp.Keys[0], nil
}

// RevokeKey revokes a key.
func (c *Client) RevokeKey(ctx context.Context, keyid string) error {
	return c.do(ctx, http.MethodDelete, "/admin/v1/keys/"+keyid, nil, nil)
}

// Usage returns spend by key and model.
func (c *Client) Usage(ctx context.Context) (UsageResponse, error) {
	var r UsageResponse
	if err := c.do(ctx, http.MethodGet, "/admin/v1/usage", nil, &r); err != nil {
		return UsageResponse{}, err
	}
	return r, nil
}

// Model is one entry from the gateway's OpenAI-compatible catalogue.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	// Provider is the supplier that answers; ProviderModel is the concrete
	// model it serves. The catalogue is three levels and this client read
	// only the alias, so `ship-proven models` listed "claude" with an OWNED BY
	// of "leartech" -- a constant the gateway sets on every row -- and no
	// way to see that claude-opus-4-8 was answering.
	//
	// Both omitempty: a gateway older than the change that added them sends
	// neither, and that has to read as "not reported" rather than blank.
	//
	// source: leartech-ai-gateway internal/api/types.go Model
	//
	// RENAMED ON THE WIRE. This was `provider` and held the ADAPTER; the
	// gateway renamed it to `interface` and gave `provider` its real meaning
	// (ai-gateway#78). Reading the old tag now silently yields "", which is
	// why Provider below is a separate field rather than a re-point.
	Interface     string `json:"interface,omitempty"`
	ProviderModel string `json:"provider_model,omitempty"`

	// Provider is WHOSE model it is; Hosting is where the weights run.
	//
	// glm, codestral and qwen-via-litellm share one interface and are z.ai,
	// Mistral and our own Ollama.
	// proven-by: TestModel_ProviderAndHostingAreSeparateWireFields
	// proven-by: TestModel_AnOlderGatewayLeavesProvenanceAbsent
	Provider string `json:"provider,omitempty"`
	Hosting  string `json:"hosting,omitempty"`
	// MaxCtx and Vision were on the wire all along and silently discarded
	// here. The gateway sends them so a caller can cap a prompt that would
	// otherwise be truncated at the provider; dropping them is the same
	// wire-shape miss as reading scp as a string.
	//
	// proven-by: TestModel_ProviderAndHostingAreSeparateWireFields
	MaxCtx int  `json:"max_ctx,omitempty"`
	Vision bool `json:"vision"`
}

// Served is what a Model's concrete name is known to be.
type Served int

const (
	// ServedUnreported means the gateway did not say. Older gateways omit
	// provider_model entirely, and that is not the same as "no indirection".
	ServedUnreported Served = iota
	// ServedDirectly means the alias IS the concrete name (echo, qwen).
	ServedDirectly
	// ServedIndirectly means the alias resolves to a different model.
	ServedIndirectly
)

// Serves reports the concrete model behind an alias and how well that is known.
//
// Three states, not two. Collapsing unreported into direct rendered an older
// gateway's silence as "(itself)" -- a positive claim that the alias is the
// concrete model, made from no information at all. A test written for the
// older-gateway case caught it.
//
// proven-by: TestModel_ServesDistinguishesUnreportedFromDirect
func (m Model) Serves() (string, Served) {
	switch m.ProviderModel {
	case "":
		return "", ServedUnreported
	case m.ID:
		return m.ID, ServedDirectly
	default:
		return m.ProviderModel, ServedIndirectly
	}
}

type modelsResponse struct {
	Data []Model `json:"data"`
}

// Models lists what this cluster says it can serve.
//
// Advertised, not proven callable: the catalogue is per-tenant and a
// caller's own key may still be refused by its model_allowlist. On
// 2026-09-14 this endpoint listed qwen on gcp while the backing Ollama had
// had no endpoints for 172 days.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var r modelsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/models", nil, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// ChatMessage is one turn.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequestMessage is one OUTBOUND turn.
//
// Separate from ChatMessage deliberately. ChatMessage is what the gateway
// SENDS back, and cache_control is something a caller SENDS -- putting a
// request-only field on the shared type made it unpopulatable from any
// response, which TestChatResponse_DecodesEveryFieldTheGatewaySends caught
// immediately. The guard was right: one type serving both directions hides
// which fields belong to which.
type ChatRequestMessage struct {
	Role    string
	Content string

	// Blocks carries OpenAI content blocks instead of Content when non-empty.
	// A plain string has nowhere to put a per-block attribute, and
	// cache_control is one: it marks which part of the prompt is stable.
	//
	// proven-by: TestChatRequestMessage_BlocksReplaceTheStringRatherThanJoiningIt
	// proven-by: TestCacheControl_RidesOnTheBlockNotTheMessage
	Blocks []ContentBlock

	// proven-by: TestChatRequest_CarriesToolsAndToolResults
	// ToolCalls is what an assistant turn ASKED for, replayed beside the
	// answer.
	ToolCalls json.RawMessage

	// ToolCallID ties a result to the call it answers. Several calls can be
	// outstanding at once, so position is not identity.
	// proven-by: TestChatRequest_CarriesToolsAndToolResults
	ToolCallID string
}

// MarshalJSON emits content either as a string or as a block array.
//
// proven-by: TestChatRequestMessage_PlainContentStaysAString
// proven-by: TestChatRequestMessage_BlocksReplaceTheStringRatherThanJoiningIt
//
// Sending both is ambiguous at the far end, and the gateway rebuilds
// blocks from whichever it finds -- so a message carrying each would silently
// resolve to one of them.
func (m ChatRequestMessage) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role       string          `json:"role"`
		Content    any             `json:"content,omitempty"`
		ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	w := wire{Role: m.Role, Content: m.Content,
		ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	if len(m.Blocks) > 0 {
		w.Content = m.Blocks
	}
	return json.Marshal(w)
}

// ContentBlock is one OpenAI content block. cache_control rides here rather
// than on the message, because a cache breakpoint marks a position in the
// prompt: everything up to and including the marked block is the cacheable
// prefix.
type ContentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// CacheControl marks a prefix for caching.
//
// TTL selects the write tier and the tiers are priced differently -- 5 minutes
// is the default, 1 hour costs more to establish. Omitted travels as omitted,
// so a caller who did not choose the dearer tier is not billed for it.
//
// proven-by: TestCacheControl_AnOmittedTTLIsNotInvented
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// ChatRequest is POST /v1/chat/completions.
type ChatRequest struct {
	Model     string               `json:"model"`
	Messages  []ChatRequestMessage `json:"messages"`
	MaxTokens int                  `json:"max_tokens,omitempty"`

	// Tools are function definitions in OpenAI's shape, forwarded verbatim.
	//
	// Raw because the SCHEMA inside belongs to whoever wrote the tool, and a
	// mismatch would be silent rather than loud.
	//
	// source: leartech-ai-gateway internal/api/types.go ChatCompletionRequest
	// proven-by: TestChatRequest_CarriesToolsAndToolResults
	Tools json.RawMessage `json:"tools,omitempty"`
}

// ChatUsage is what the call cost.
//
// ONE CONVENTION: every detail field is a SUBSET of PromptTokens, and the
// uncached remainder is PromptTokens minus the rest.
// proven-by: TestChatUsage_TheCacheSubsetsFitInsidePromptTokens
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// Pointers: ABSENT and ZERO are different facts.
	// proven-by: TestChatUsage_AnAbsentCacheIsNotAZeroCache
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	LeartechCache       *LeartechCache       `json:"leartech_cache,omitempty"`
}

// PromptTokensDetails is OpenAI's own breakdown. Reads live here rather than in
// a leartech field so an OpenAI client understands them with no special-casing.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// LeartechCache carries the cache WRITE counts, one field per TTL tier.
// proven-by: TestChatUsage_TheTwoWriteTiersAreDistinct
type LeartechCache struct {
	Write5mTokens int `json:"write_5m_tokens"`
	Write1hTokens int `json:"write_1h_tokens"`
}

// CacheReported says whether the supplier reports cache information at all.
// proven-by: TestChatUsage_AnAbsentCacheIsNotAZeroCache
func (u ChatUsage) CacheReported() bool {
	return u.PromptTokensDetails != nil || u.LeartechCache != nil
}

// CachedTokens is the read subset.
func (u ChatUsage) CachedTokens() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// CacheWrite5mTokens is the 5-minute write subset.
func (u ChatUsage) CacheWrite5mTokens() int {
	if u.LeartechCache == nil {
		return 0
	}
	return u.LeartechCache.Write5mTokens
}

// CacheWrite1hTokens is the 1-hour write subset.
func (u ChatUsage) CacheWrite1hTokens() int {
	if u.LeartechCache == nil {
		return 0
	}
	return u.LeartechCache.Write1hTokens
}

// FreshTokens is the uncached remainder, derived rather than reported.
//
// proven-by: TestChatUsage_FreshIsTheRemainderNotAReportedNumber
func (u ChatUsage) FreshTokens() int {
	return u.PromptTokens - u.CachedTokens() - u.CacheWrite5mTokens() - u.CacheWrite1hTokens()
}

// ChatChoice is one completion.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatResponse is the completion the gateway returned.
type ChatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   ChatUsage    `json:"usage"`
}

// Reply is the assistant's text, or "" with false if the response carried
// no choice.
//
// A completion with no choices is a 200 that said nothing, and returning
// an empty string as though the model had replied emptily would be a
// different answer from "the gateway returned nothing".
func (r ChatResponse) Reply() (string, bool) {
	if len(r.Choices) == 0 {
		return "", false
	}
	return r.Choices[0].Message.Content, true
}

// Chat sends a completion request. The credential may be a JWT or an
// sk-lt- virtual key: /v1/chat/completions accepts either.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var r ChatResponse
	if err := c.do(ctx, http.MethodPost, "/v1/chat/completions", req, &r); err != nil {
		return ChatResponse{}, err
	}
	return r, nil
}

// PricingModel is one catalogued model and what it costs to call.
type PricingModel struct {
	ID        string `json:"id"`
	Provider  string `json:"provider,omitempty"`
	Hosting   string `json:"hosting,omitempty"`
	Interface string `json:"interface,omitempty"`
	// Rates is micros per 1000 tokens keyed by kind (input, output,
	// cache_read, cache_write_5m, cache_write_1h). A kind ABSENT here has no
	// rate on file; it is NOT zero, and rendering it as 0 is the conflation
	// the endpoint exists to undo.
	Rates map[string]int64 `json:"rates"`
	// Unpriced reports a model that is callable and has no input rate.
	//
	// A missing rate resolves to zero in the router's cost ranking, so an
	// unpriced model reads as FREE and `auto` prefers it over every real
	// supplier. The only prior symptom was traffic silently moving.
	Unpriced bool `json:"unpriced"`
}

// PricingResponse is GET /admin/v1/pricing.
type PricingResponse struct {
	Object string `json:"object"`
	// Unit is carried rather than assumed. The gateway says micros_per_1k
	// today; a client that hard-codes that divisor prints a number off by
	// a thousand the day it changes, with no error anywhere.
	Unit          string         `json:"unit"`
	Data          []PricingModel `json:"data"`
	UnpricedCount int            `json:"unpriced_count"`
}

// Pricing returns the rate table behind `auto`.
//
// JWT-only at the gateway: usage_read is MethodJWT in its registry, so a
// virtual key reaching this path is refused on auth_method rather than on
// scope.
func (c *Client) Pricing(ctx context.Context) (PricingResponse, error) {
	var r PricingResponse
	if err := c.do(ctx, http.MethodGet, "/admin/v1/pricing", nil, &r); err != nil {
		return PricingResponse{}, err
	}
	return r, nil
}
