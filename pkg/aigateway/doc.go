// Package aigateway is the estate's only client for talking to a model.
//
// # The rule
//
// Every model call in the estate goes through leartech-ai-gateway, and every
// caller reaches it through this package. Not "should" — the package refuses
// to construct a client for a process that has provider-native
// configuration set, because that is the only enforcement that survives a
// hurried change.
//
// # Why it exists
//
// Before it, each caller reached models its own way. The agents pointed the
// Anthropic SDK at the gateway with ANTHROPIC_BASE_URL. The ship-proven CLI
// held its own client under internal/, which Go forbade anyone else from
// importing, so nobody could reuse it even having found it. The Tekton
// reviewer did something else again. Every one of those is a place the
// gateway can be bypassed, and four places to reimplement the things that
// turn out to matter:
//
//   - CORRELATION. The gateway records a caller's own run_id and session_id
//     on the usage row that carries cost_micros. Without it a model call is
//     metered correctly and attributable to nobody. Config.Client wires it
//     unconditionally, because a caller that has to remember is a caller
//     whose rows are unattributable the first time someone forgets.
//
//   - PROMPT CACHING. A conversation re-sends its whole history every turn,
//     and marking the growing prefix took 41,169 prompt tokens down to 81
//     fresh ones — measured on 2026-09-26, 99% served from cache. A caller
//     that builds its own requests does not get that.
//
//   - USAGE. A streamed turn carries its token counts only on a trailing
//     frame the request has to ask for. Three separate things had to be
//     right before the ship-proven shell could see what a turn cost, and
//     every new caller would have had to get all three right too.
//
// # Configuration
//
// LEARTECH_AIGW_URL and LEARTECH_AIGW_API_KEY are required;
// LEARTECH_AIGW_MODEL is the default model; LEARTECH_RUN_ID or
// LEARTECH_SESSION_ID is the correlation key. Nothing else. Construction
// fails closed on anything missing, matching pkg/auth: a gateway URL
// guessed from a constant is a request sent somewhere nobody chose.
//
// # Credentials
//
// A virtual key is required today, and a bearer token is not a substitute.
// The gateway resolves a spend cap from the credential, populates one only
// for a virtual key, and refuses a caller whose cap it fails to resolve
// rather than treating it as unlimited — so a JWT gets a caller
// authenticated and then refused at the spend gate, which reads as an auth
// fault and is not one. When the tenant-policy source lands in
// auth-service, LEARTECH_AIGW_API_KEY becomes optional and pkg/auth's
// ServiceAuthClient is sufficient on its own.
//
// source: leartech-ai-gateway internal/api/handlers.go resolveLimits and
// internal/authz/limits.go ResolveLimits.
//
// # Relationship to pkg/auth
//
// They answer different questions and both are usually needed. pkg/auth
// gets a workload a token for calling another leartech SERVICE. This
// package gets it to a MODEL. An agent uses both: client_credentials for
// plan-api, a virtual key for the gateway.
package aigateway
