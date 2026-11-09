package forge

import "context"

// AdminForge is the provisioning surface: creating repos and inviting
// collaborators. It is a SEPARATE interface from Forge on purpose — Forge is the
// read/PR surface every consumer gets; AdminForge is what repo_factory needs, and
// a deployment can wire one without the other.
//
// Shapes are GitHub-compatible; Gitea/Forgejo implements them natively.
type AdminForge interface {
	Forge

	// AuthenticatedUser returns the login of the token's identity.
	AuthenticatedUser(ctx context.Context) (string, error)

	// CreateUserRepo creates a repo under the AUTHENTICATED user.
	CreateUserRepo(ctx context.Context, name, description string, private, autoInit bool) (Repo, error)

	// CreateOrgRepo creates a repo under an org.
	CreateOrgRepo(ctx context.Context, org, name, description string, private, autoInit bool) (Repo, error)

	// AddCollaborator invites username to owner/repo at the given permission
	// (pull|triage|push|maintain|admin — Gitea accepts read|write|admin and the
	// driver maps).
	AddCollaborator(ctx context.Context, repo Repo, username, permission string) error
}
