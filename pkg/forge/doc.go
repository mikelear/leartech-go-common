// Package forge is the platform's forge-neutral client: one interface over
// GitHub and Gitea/Forgejo, so every consumer that posts to or reads from a
// pull request — the catalog's forge CLI (leartech-forge), the MCP PR tools
// (leartech-mcp-servers), and the run recorder (leartech-agent-run-recorder) —
// shares ONE tested implementation rather than three hand-rolled api.github.com
// clients.
//
// # Why this exists
//
// Before this package the estate had the GitHub REST shape hard-coded in at
// least four MCP packages, the recorder's three sinks, and a dozen catalog
// shell `curl api.github.com` calls — each with its own base-URL default, its
// own header convention, and its own ad-hoc error handling. Moving a repo to a
// self-hosted Forgejo meant finding and changing all of them. A Forge value
// selected by Kind + BaseURL replaces that: the caller picks the driver from
// config (gitKind / gitServer) and does not branch on the forge again.
//
// GitHub and Forgejo/Gitea expose nearly the same REST resources
// (/repos/{owner}/{repo}/issues/{n}/comments, /pulls, …); the drivers absorb
// the small differences. Targeting the "Gitea API" shape for the non-GitHub
// driver means one driver serves both Forgejo and Gitea.
//
// # Errors are typed, not stringly
//
// Methods return wrapped sentinel errors (ErrNotFound, ErrUnauthorized,
// ErrForbidden, ErrConflict, ErrRateLimited, ErrUnavailable) so callers decide
// on errors.Is rather than on substring matching. The remote's own message travels
// in the wrapped text; the sentinel says only what class of failure it is and
// therefore what the remedy is. Same discipline as go-common/pkg/aigateway.
package forge
