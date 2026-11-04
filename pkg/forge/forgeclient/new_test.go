package forgeclient_test

import (
	"errors"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
	"github.com/mikelear/leartech-go-common/pkg/forge/forgeclient"
)

func TestNew_SelectsDriverByKind(t *testing.T) {
	gh, err := forgeclient.New(forge.KindGitHub, "", "t", nil)
	if err != nil || gh.Kind() != forge.KindGitHub {
		t.Fatalf("github: %v / %v", gh, err)
	}
	gt, err := forgeclient.New(forge.KindGitea, "https://f", "t", nil)
	if err != nil || gt.Kind() != forge.KindGitea {
		t.Fatalf("gitea: %v / %v", gt, err)
	}
}

func TestNew_UnknownKindIsUnsupported(t *testing.T) {
	_, err := forgeclient.New(forge.Kind("gitlab"), "", "t", nil)
	if !errors.Is(err, forge.ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}
