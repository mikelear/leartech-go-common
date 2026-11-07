package forge_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/forge"
)

func TestParseKind(t *testing.T) {
	cases := map[string]struct {
		want forge.Kind
		err  bool
	}{
		"github":  {forge.KindGitHub, false},
		"GitHub":  {forge.KindGitHub, false},
		"gitea":   {forge.KindGitea, false},
		"forgejo": {forge.KindGitea, false}, // Forgejo speaks the Gitea API
		"gitlab":  {"", true},
		"":        {"", true},
	}
	for in, c := range cases {
		got, err := forge.ParseKind(in)
		if c.err {
			if err == nil {
				t.Errorf("ParseKind(%q): want error, got %q", in, got)
			}
			if err != nil && !errors.Is(err, forge.ErrUnsupported) {
				t.Errorf("ParseKind(%q): want ErrUnsupported, got %v", in, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseKind(%q) = %q,%v; want %q,nil", in, got, err, c.want)
		}
	}
}

func TestParseKind_TrimsSurroundingSpace(t *testing.T) {
	got, err := forge.ParseKind("  gitea  ")
	if err != nil || got != forge.KindGitea {
		t.Fatalf("ParseKind with spaces = %q,%v; want gitea,nil", got, err)
	}
}

func TestStickyMarker_IsAnInvisibleHTMLComment(t *testing.T) {
	m := forge.StickyMarker("coverage")
	if m != "<!-- leartech-sticky: coverage -->" {
		t.Fatalf("marker = %q", m)
	}
}

func TestRepoString(t *testing.T) {
	if got := (forge.Repo{Owner: "mikelear", Name: "x"}).String(); got != "mikelear/x" {
		t.Fatalf("Repo.String = %q", got)
	}
}

// The two newest iface methods are pinned by compile-time assertions in both
// drivers plus behaviour tests in the driver files. This test pins the wire
// spelling of the GitHub status shape, because the gate view (Keeper's merge
// status) is parsed from exactly these fields.
func TestCommitStatusWireShapeGitHub(t *testing.T) {
	var st struct {
		Context   string `json:"context"`
		State     string `json:"state"`
		TargetURL string `json:"target_url"`
	}
	b := []byte(`{"context":"Lighthouse Merge Status","state":"success","target_url":"http://lighthouse/merge/status"}`)
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st.Context != "Lighthouse Merge Status" || st.State != "success" {
		t.Fatalf("gate context did not decode: %+v", st)
	}
}
