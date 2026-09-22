package ci

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// THE LOCAL GATE IS ONLY USEFUL IF IT IS THE SAME GATE.
//
// The Makefile exists so a developer can run what CI runs before pushing. It
// overrides COVERAGE_SCOPE and COVERAGE_THRESHOLD because the golden
// leartech-go.mk defaults to ./internal/... and 60.0, and this library has no
// internal/ and an agreed floor of 85.
//
// Two values in two files, with nothing connecting them, drift. If test.yaml
// raises the floor and the Makefile does not, `make verify` passes and the PR
// fails — which teaches people that the local gate cannot be trusted, and the
// point of having one is lost. This is that connection.
const makefile = "../Makefile"

func TestMakefileCoverageSettingsMatchCI(t *testing.T) {
	src := readMakefile(t)

	rawFloor := readMakefileVar(t, src, "COVERAGE_THRESHOLD")
	gotFloor, err := strconv.ParseFloat(rawFloor, 64)
	if err != nil {
		t.Fatalf("Makefile COVERAGE_THRESHOLD %q is not a number: %v", rawFloor, err)
	}
	if wantFloor := readThreshold(t); gotFloor != wantFloor {
		t.Errorf("Makefile COVERAGE_THRESHOLD is %.1f but %s says %.1f.\n"+
			"A local run that enforces a different floor than CI is worse than no local "+
			"run: it reports success for a tree CI will reject.",
			gotFloor, testYAML, wantFloor)
	}

	gotScope := readMakefileVar(t, src, "COVERAGE_SCOPE")
	wantScope := readScope(t)
	if gotScope != wantScope {
		t.Errorf("Makefile COVERAGE_SCOPE is %q but %s says %q.\n"+
			"Measuring a different package set locally means the percentage the developer "+
			"sees is not the percentage the gate computes.",
			gotScope, testYAML, wantScope)
	}
}

// The golden mk's defaults are wrong for this repo, so the overrides must
// actually be present. A Makefile that stopped setting them would fall back to
// ./internal/... — which does not exist here, reports total=0.0% and fails the
// floor. Loud, but for a reason nobody would guess from the output.
func TestMakefileSetsTheCoverageOverridesAtAll(t *testing.T) {
	src := readMakefile(t)
	for _, v := range []string{"COVERAGE_SCOPE", "COVERAGE_THRESHOLD"} {
		if !regexp.MustCompile(`(?m)^` + v + `\s*\??=`).MatchString(src) {
			t.Errorf("the Makefile no longer sets %s, so a local run uses the golden "+
				"default and measures the wrong thing", v)
		}
	}
}

func readMakefile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(makefile)
	if err != nil {
		t.Fatalf("unable to read %s: %v", makefile, err)
	}
	return string(b)
}

func readMakefileVar(t *testing.T, src, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + name + `\s*\??=\s*(\S+)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no %s assignment in %s; this test reads a variable that has moved, "+
			"so a pass would mean nothing", name, makefile)
	}
	return m[1]
}

func readScope(t *testing.T) string {
	t.Helper()
	src := readYAML(t)
	m := regexp.MustCompile(`(?s)name:\s*COVERAGE_SCOPE\s*\n\s*value:\s*"([^"]+)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no COVERAGE_SCOPE found in " + testYAML + "; this test reads a key that " +
			"has moved, so a pass would mean nothing")
	}
	return m[1]
}
