package agentloop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxWriteBytes bounds one write.
//
// The content arrives in a tool call, so it was already paid for on the
// way up; the cap is about what a person can meaningfully approve.
const maxWriteBytes = 1 << 20

// WriteFile replaces a file's contents, with the change shown first.
//
// WHOLE FILE, NOT A TARGETED EDIT. A string replacement is cheaper and
// can match in more than one place, and its failure is a silent edit
// somewhere nobody looked. Supplying the whole file means what is
// approved is exactly what will exist.
//
// proven-by: TestWriteFile_WritesUnderTheRoot
// proven-by: TestWriteFile_RefusesToEscapeTheRoot
// proven-by: TestWriteFile_RefusesASymlinkPointingOut
// proven-by: TestWriteFile_RefusesGitInternals
// proven-by: TestWriteFile_CreatesMissingParentDirectories
// proven-by: TestWriteFile_RefusesADirectory
// proven-by: TestWriteFile_RefusesContentTooLargeToApprove
func WriteFile(root string) Tool {
	return Tool{
		Name:   "local__write_file",
		Effect: Writes,
		Description: "Write a file in the working directory, replacing it " +
			"entirely. Supply the complete new contents; the change is shown " +
			"to the operator as a diff and approved before anything is written.",
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{
		    "path":{"type":"string","description":"path relative to the working directory"},
		    "content":{"type":"string","description":"the complete new contents of the file"}
		  },
		  "required":["path","content"]
		}`),
		Preview: func(args json.RawMessage) string {
			return writePreview(root, args)
		},
		Run: func(args json.RawMessage) (string, error) {
			in, err := parseWrite(args)
			if err != nil {
				return "", err
			}
			target, err := resolveForWrite(root, in.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return "", fmt.Errorf("creating the directory: %w", err)
			}
			existing, had := readIfPresent(target)
			if err := os.WriteFile(target, []byte(in.Content), 0o600); err != nil {
				return "", err
			}
			if !had {
				return fmt.Sprintf("wrote %s (new file, %d bytes)", in.Path, len(in.Content)), nil
			}
			return fmt.Sprintf("wrote %s (%d bytes, was %d)",
				in.Path, len(in.Content), len(existing)), nil
		},
	}
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func parseWrite(args json.RawMessage) (writeArgs, error) {
	var in writeArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return in, fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if in.Path == "" {
		return in, errors.New("path is required")
	}
	if len(in.Content) > maxWriteBytes {
		return in, fmt.Errorf("%d bytes is more than the %d-byte limit; a change "+
			"that large is not reviewable in a prompt", len(in.Content), maxWriteBytes)
	}
	return in, nil
}

// writePreview is what the operator is asked to approve.
//
// THE DIFF, NOT THE ARGUMENTS. Rendering the raw JSON would show the
// whole new file escaped onto one line, which is unreadable precisely
// when reading it matters. A path that will be refused says so here too,
// rather than at the moment of writing — approving something that then
// fails teaches nothing about what was wrong.
//
// proven-by: TestWritePreview_ShowsADiffAgainstTheCurrentFile
// proven-by: TestWritePreview_ANewFileSaysItIsNew
// proven-by: TestWritePreview_ARefusalIsVisibleBeforeApproval
func writePreview(root string, args json.RawMessage) string {
	in, err := parseWrite(args)
	if err != nil {
		return "cannot be written: " + err.Error()
	}
	target, err := resolveForWrite(root, in.Path)
	if err != nil {
		return "cannot be written: " + err.Error()
	}

	existing, had := readIfPresent(target)
	if !had {
		body := in.Content
		lines := strings.Count(strings.TrimSuffix(body, "\n"), "\n") + 1
		if body == "" {
			lines = 0
		}
		head := fmt.Sprintf("%s — NEW FILE, %d line(s)", in.Path, lines)
		if dir := filepath.Dir(in.Path); dir != "." && !dirExists(filepath.Dir(target)) {
			// Creating directories is a side effect beyond the file
			// itself, so it is named rather than done quietly.
			head += fmt.Sprintf("\ncreates directory %s/", dir)
		}
		return head + "\n" + unifiedDiff("", body)
	}
	if string(existing) == in.Content {
		return in.Path + " — no change; the contents are already exactly this"
	}
	return in.Path + "\n" + unifiedDiff(string(existing), in.Content)
}

func readIfPresent(path string) ([]byte, bool) {
	b, err := os.ReadFile(path) // #nosec G304 -- resolved and proven under root
	if err != nil {
		return nil, false
	}
	return b, true
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolveForWrite contains a path that does not exist yet.
//
// resolveUnder resolves symlinks on the full path, which fails for a
// file about to be created. // proven-by: TestResolveForWrite_ContainsAPathThatDoesNotExistYet
// So the deepest EXISTING ancestor is resolved instead and the
// remainder appended: symlinks in the part that exists are still
// followed, and a parent pointing outside the root is still caught.
//
// proven-by: TestWriteFile_RefusesToEscapeTheRoot
// proven-by: TestWriteFile_RefusesASymlinkPointingOut
// proven-by: TestResolveForWrite_ContainsAPathThatDoesNotExistYet
func resolveForWrite(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("working directory is unreadable: %w", err)
	}
	full := filepath.Join(realRoot, rel)

	// Walk up to the first ancestor that exists, resolve THAT, then put
	// the missing tail back on.
	existing, missing := full, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("refusing to write %s: no part of its path exists", rel)
		}
		missing = filepath.Join(filepath.Base(existing), missing)
		existing = parent
	}
	resolvedBase, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !under(realRoot, resolvedBase) {
		return "", fmt.Errorf("refusing to write %s: it resolves outside the "+
			"working directory", rel)
	}
	target := filepath.Join(resolvedBase, missing)
	if !under(realRoot, target) {
		return "", fmt.Errorf("refusing to write %s: it resolves outside the "+
			"working directory", rel)
	}
	if insideGitDir(realRoot, target) {
		return "", fmt.Errorf("refusing to write %s: .git is repository internals "+
			"rather than the project", rel)
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory", rel)
	}
	return target, nil
}
