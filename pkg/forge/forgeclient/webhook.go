package forgeclient

import (
	"fmt"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/gitea"
	"github.com/mikelear/leartech-go-common/pkg/forge/drivers/github"
)

// Compile-time assertion that both drivers satisfy the hook surface, made here rather than in
// each driver's package so a driver that drops it fails once, at the seam a caller uses.
var (
	_ forge.Webhooker = (*github.Client)(nil)
	_ forge.Webhooker = (*gitea.Client)(nil)
)

// Webhooks returns f as a Webhooker, or an error naming the kind.
//
// A consumer holding a forge.Forge (from New) reaches the hook surface through this call,
// because registering a hook needs repo admin and Forge is the read credential's shape. One
// conversion, so a caller does not type-assert.
// proven-by: TestWebhooks_NamesTheKindWhenUnsupported
func Webhooks(f forge.Forge) (forge.Webhooker, error) {
	if w, ok := f.(forge.Webhooker); ok {
		return w, nil
	}
	return nil, fmt.Errorf("forge %q does not implement the webhook surface", f.Kind())
}
