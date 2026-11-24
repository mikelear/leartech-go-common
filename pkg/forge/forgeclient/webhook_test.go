package forgeclient_test

import (
	"context"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/forgeclient"
)

// The conversion exists so a caller does not type-assert, and it must fail with a message
// that names the kind when a driver lacks the surface.
//
// proven-by: TestWebhooks_NamesTheKindWhenUnsupported
func TestWebhooks_NamesTheKindWhenUnsupported(t *testing.T) {
	if _, err := forgeclient.Webhooks(noHooks{}); err == nil {
		t.Fatal("want an error for a driver without the hook surface")
	}
}

func TestWebhooks_ReturnsTheDriverThatHasIt(t *testing.T) {
	f, err := forgeclient.New(forge.KindGitea, "https://forgejo.example", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forgeclient.Webhooks(f); err != nil {
		t.Fatalf("the gitea driver must satisfy the hook surface: %v", err)
	}
}

// noHooks is a Forge with no hook surface: the negative control for the assertion above.
type noHooks struct{ forge.Forge }

func (noHooks) Kind() forge.Kind { return "test" }

var _ = context.Background
