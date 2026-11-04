// Package forgeclient wires the forge.Forge interface to its concrete drivers.
//
// It is separate from package forge to avoid an import cycle: the drivers import
// forge for the interface + sentinels, so forge itself does not import the
// drivers. Consumers that want a ready Forge import forgeclient; consumers that
// only need the types (e.g. to accept a forge.Forge parameter) import forge.
package forgeclient

import (
	"net/http"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/gitea"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/github"
)

// New returns the Forge for kind, presenting token at base. This is the one seam
// a consumer uses: resolve kind + base from config (gitKind / gitServer) and
// not branch on the forge again.
//
//   - forge.KindGitHub: base is the API root (empty → api.github.com).
//   - forge.KindGitea:  base is the instance root (the driver adds /api/v1).
func New(kind forge.Kind, base, token string, h *http.Client) (forge.Forge, error) {
	switch kind {
	case forge.KindGitHub:
		return github.New(base, token, h), nil
	case forge.KindGitea:
		return gitea.New(base, token, h), nil
	default:
		return nil, forge.ParseKindError(string(kind))
	}
}
