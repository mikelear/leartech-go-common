package agentloop_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikelear/leartech-go-common/pkg/agentloop"
)

// realFile writes n numbered lines and returns the root it lives under.
// A real file on a real filesystem, because every defect this tool has had
// was in the boundary between a path and the disk, and a fake reader has no
// boundary to get wrong.
func realFile(t *testing.T, name string, n int) string {
	t.Helper()
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return root
}

func readFile(t *testing.T, root string, args string) (string, error) {
	t.Helper()
	return agentloop.ReadFile(root).Run(json.RawMessage(args))
}

// A RANGE RETURNS THAT RANGE.
func TestReadFile_ReadsALineRange(t *testing.T) {
	root := realFile(t, "big.txt", 900)

	out, err := readFile(t, root, `{"path":"big.txt","offset":340,"limit":5}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"line 340", "line 341", "line 344"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "line 339") || strings.Contains(out, "line 345") {
		t.Errorf("returned lines outside the range:\n%s", out)
	}
}

// A RANGE SAYS IT IS A RANGE.
//
// A bare slice looks exactly like a whole file. A model that believes it has
// read everything draws conclusions from what is absent.
func TestReadFile_ARangeSaysWhatItIs(t *testing.T) {
	root := realFile(t, "big.txt", 900)

	out, err := readFile(t, root, `{"path":"big.txt","offset":340,"limit":5}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	head := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"340", "344", "900"} {
		if !strings.Contains(head, want) {
			t.Errorf("the header %q does not state %s (range and true length)", head, want)
		}
	}
}

// A RANGE READS A FILE THE WHOLE-FILE PATH REFUSES.
//
// readUnder caps at 256KB and returns an error, so today a large generated
// file cannot be inspected at all. The cap protects the prompt; it should
// not also be a wall.
func TestReadFile_ARangeReadsBeyondTheWholeFileCap(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 40000; i++ { // ~ 470KB, comfortably over the cap
		fmt.Fprintf(&b, "line %d padding padding padding\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "huge.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if _, err := readFile(t, root, `{"path":"huge.txt"}`); err == nil {
		t.Fatal("the whole-file read accepted a file over the cap; this test no longer proves anything")
	}
	out, err := readFile(t, root, `{"path":"huge.txt","offset":39998,"limit":2}`)
	if err != nil {
		t.Fatalf("a ranged read of a large file failed: %v", err)
	}
	if !strings.Contains(out, "line 39998") {
		t.Errorf("did not return the requested range:\n%s", out)
	}
}

// PAST THE END IS AN ERROR, NOT AN EMPTY ANSWER.
//
// Empty output reads as "there is nothing there", which is a claim about the
// file rather than about the request.
func TestReadFile_AnOffsetPastTheEndSaysSo(t *testing.T) {
	root := realFile(t, "small.txt", 10)

	_, err := readFile(t, root, `{"path":"small.txt","offset":50,"limit":5}`)
	if err == nil {
		t.Fatal("an offset past the end returned success")
	}
	if !strings.Contains(err.Error(), "10") || !strings.Contains(err.Error(), "50") {
		t.Errorf("the error names neither the length nor the offset: %v", err)
	}
}

// A LIMIT RUNNING OFF THE END STOPS AT THE END.
func TestReadFile_ALimitBeyondTheEndStopsAtTheEnd(t *testing.T) {
	root := realFile(t, "small.txt", 10)

	out, err := readFile(t, root, `{"path":"small.txt","offset":8,"limit":500}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "line 10") {
		t.Errorf("did not reach the last line:\n%s", out)
	}
	if !strings.Contains(strings.SplitN(out, "\n", 2)[0], "8-10") {
		t.Errorf("the header does not report the truncated range:\n%s", out)
	}
}

// NO RANGE STILL MEANS THE WHOLE FILE, UNCHANGED AND UNHEADED.
//
// Every existing caller passes only a path. A header appearing on those
// would change what the shell prints and what the model is handed.
func TestReadFile_NoRangeIsUnchanged(t *testing.T) {
	root := realFile(t, "small.txt", 3)

	out, err := readFile(t, root, `{"path":"small.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != "line 1\nline 2\nline 3\n" {
		t.Errorf("the whole-file read changed shape: %q", out)
	}
}

// A NEGATIVE RANGE IS REFUSED.
func TestReadFile_ANegativeRangeIsRefused(t *testing.T) {
	root := realFile(t, "small.txt", 10)

	if _, err := readFile(t, root, `{"path":"small.txt","offset":-1,"limit":5}`); err == nil {
		t.Error("a negative offset was accepted")
	}
	if _, err := readFile(t, root, `{"path":"small.txt","offset":1,"limit":-5}`); err == nil {
		t.Error("a negative limit was accepted")
	}
}

// A RANGE OBEYS THE SAME BOUNDARY AS EVERY OTHER PATH TOOL.
func TestReadFile_ARangeCannotEscapeTheRoot(t *testing.T) {
	root := realFile(t, "small.txt", 3)

	if _, err := readFile(t, root, `{"path":"../etc/passwd","offset":1,"limit":1}`); err == nil {
		t.Fatal("a ranged read escaped the working directory")
	}
}
