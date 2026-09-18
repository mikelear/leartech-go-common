// Package ci holds tests about how this repository is BUILT, rather than what
// it does at runtime. There is no production code here.
package ci

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// THE FLOOR HAS TO COME FROM THE NUMBER THE GATE COMPUTES.
//
// It sat at 60 against a real 86.2% — about 26 points of slack, enough for
// pkg/auth to lose a third of its coverage without failing. Nobody chose that
// badly: they read the figures recorded in test.yaml, and those were RAW
// numbers from a plain `go test -cover`.
//
// leartech-go.mk strips generated files before computing the total. A
// generated mock in pkg/auth is large enough to move the answer 23 points:
//
//	raw       62.9%
//	stripped  86.2%   <- what CI enforces against
//
// So the failure mode is not someone lowering the floor dishonestly. It is
// someone re-measuring the easy way, believing the smaller number, and
// "correcting" the floor down to match it. These tests make that a build
// failure instead of a quiet regression.

const testYAML = "../.lighthouse/jenkins-x/test.yaml"

// The floor the estate agreed for this library, 2026-09-18. Lowering it is a
// decision, and this test is where that decision has to be made explicitly.
const agreedFloor = 85.0

func TestCoverageFloorIsNotQuietlyLowered(t *testing.T) {
	got := readThreshold(t)
	if got < agreedFloor {
		t.Errorf("COVERAGE_THRESHOLD is %.1f, below the agreed floor of %.1f.\n\n"+
			"If a real measurement says the floor is too high, re-measure the way CI does "+
			"before changing it — a plain `go test -cover` reports ~63%% here because it "+
			"does not strip generated files, and believing that number is how the floor "+
			"came to be 60 against an actual 86.2%%:\n\n"+
			"  go test ./... -race -coverpkg=./pkg/... -cover -args -test.gocoverdir=$D\n"+
			"  go tool covdata textfmt -i=$D -pkg=<module>/... -o=c.out\n"+
			"  grep -vFf <(generated files) c.out > s.out\n"+
			"  go tool cover -func=s.out\n\n"+
			"Then change agreedFloor here in the same commit, so the number and the "+
			"reason move together.", got, agreedFloor)
	}
}

// A floor far below the real figure is the shape that hid here for months: it
// reads as enforcement while permitting a large regression. This does not
// assert the live coverage — that needs the full instrumented run — it asserts
// the floor and the recorded measurement have not drifted apart.
func TestTheRecordedMeasurementMatchesTheFloor(t *testing.T) {
	src := readYAML(t)

	m := regexp.MustCompile(`stripped \(what CI computes\)\s+([0-9.]+)%`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("test.yaml no longer records the stripped measurement. That figure is the " +
			"only thing connecting the floor to reality; without it the next person has " +
			"nothing to check the floor against, which is how it ended up at 60.")
	}
	measured, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("recorded measurement %q is not a number: %v", m[1], err)
	}

	floor := readThreshold(t)
	switch {
	case floor > measured:
		t.Errorf("the floor (%.1f) is ABOVE the recorded measurement (%.1f), so the gate "+
			"fails on a tree nobody has changed", floor, measured)
	case measured-floor > 10:
		t.Errorf("the floor (%.1f) sits %.1f points below the recorded measurement (%.1f). "+
			"That much slack is what let this library run with a 26-point gap: the gate "+
			"reads as enforcement while permitting a large regression. Raise the floor, "+
			"or record why the gap is deliberate.", floor, measured-floor, measured)
	}
}

// The raw figure is recorded next to the stripped one on purpose. Without it,
// the next person runs `go test -cover`, sees a number 23 points lower, and
// has no way to tell whether coverage collapsed or they measured differently.
func TestBothMeasurementsAreRecorded(t *testing.T) {
	src := readYAML(t)
	for _, want := range []string{
		`raw (local go test -cover)`,
		`stripped (what CI computes)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("test.yaml no longer records %q. Both numbers belong together: the "+
				"gap between them IS the finding, and recording only one reproduces the "+
				"confusion that set the floor wrong.", want)
		}
	}
}

func readYAML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(testYAML)
	if err != nil {
		t.Fatalf("unable to read %s: %v", testYAML, err)
	}
	return string(b)
}

func readThreshold(t *testing.T) float64 {
	t.Helper()
	src := readYAML(t)
	m := regexp.MustCompile(`(?s)name:\s*COVERAGE_THRESHOLD\s*\n\s*value:\s*"([0-9.]+)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no COVERAGE_THRESHOLD found in " + testYAML + "; this test reads a key that " +
			"has moved, so a pass would mean nothing")
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("COVERAGE_THRESHOLD %q is not a number: %v", m[1], err)
	}
	return v
}
