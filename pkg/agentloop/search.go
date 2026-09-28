package agentloop

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxSearchHits bounds what one search returns. The result is sent to the
// model and paid for by the token, so an unbounded answer to a broad pattern
// would cost more than the question was worth.
const maxSearchHits = 100

// maxSearchLine is the longest matching line returned in full. A minified
// bundle or a base64 blob matches patterns as readily as source does, and
// one such line can be larger than the rest of the answer together.
const maxSearchLine = 300

// Search finds a regular expression in the files under root.
//
// WHY THIS TOOL EXISTS. The local set was read_file, list_dir, glob and
// write_file: nothing searched CONTENT. Every "where is this used" therefore
// went through the shell. Measured on agent run
// shipproven-all-passed-means-all (2026-09-28), twelve of twenty-six bash
// calls were `grep -rn`, and each one costs a whole turn of re-sent
// conversation — on a run whose predecessor died at its turn ceiling.
//
// A SHELL IS NOT A SEARCH TOOL. `grep -rn x .` in a repository also walks
// .git, node_modules and every build artefact, so the model learns to append
// `| head -30`, which hides where the results stopped. This skips what nobody
// is looking for and states when it truncated. // proven-by: TestSearch_SaysWhenItTruncated
//
// proven-by: TestSearch_FindsMatchesWithFileAndLine
// proven-by: TestSearch_SkipsTheGitDirectory
// proven-by: TestSearch_SaysWhenItTruncated
func Search(root string) Tool {
	return Tool{
		Name:   "local__search",
		Effect: Reads,
		Description: "Search file CONTENTS under the working directory for a " +
			"regular expression, returning file:line: text. Optionally " +
			"restrict to a subdirectory or a filename suffix. Prefer this " +
			"over shelling out to grep: it skips .git and build output, and " +
			"it reports when results were truncated.",
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{
		    "pattern":{"type":"string","description":"Go regular expression matched against each line"},
		    "path":{"type":"string","description":"optional subdirectory to search, relative to the working directory"},
		    "suffix":{"type":"string","description":"optional filename suffix filter, e.g. .go"},
		    "ignore_case":{"type":"boolean","description":"match case-insensitively"}
		  },
		  "required":["pattern"]
		}`),
		Run: func(args json.RawMessage) (string, error) {
			var in struct {
				Pattern    string `json:"pattern"`
				Path       string `json:"path"`
				Suffix     string `json:"suffix"`
				IgnoreCase bool   `json:"ignore_case"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("arguments are not valid JSON: %w", err)
			}
			if in.Pattern == "" {
				return "", errors.New("pattern is required")
			}
			return searchUnder(root, in.Pattern, in.Path, in.Suffix, in.IgnoreCase)
		},
	}
}

// searchUnder walks the tree and matches each line.
//
// proven-by: TestSearch_RefusesToEscapeTheRoot
// proven-by: TestSearch_AnInvalidPatternSaysSo
// proven-by: TestSearch_RestrictsToASubdirectory
// proven-by: TestSearch_FiltersBySuffix
// proven-by: TestSearch_IgnoreCase
// proven-by: TestSearch_NoMatchIsNotAnError
func searchUnder(root, pattern, sub, suffix string, ignoreCase bool) (string, error) {
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("pattern %q is not a valid regular expression: %w", pattern, err)
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("working directory is unreadable: %w", err)
	}
	start := realRoot
	if sub != "" {
		// THE SAME BOUNDARY AS EVERY OTHER PATH TOOL. The model chooses this
		// argument, so it goes through resolveUnder rather than a local
		// re-derivation. // proven-by: TestSearch_RefusesToEscapeTheRoot
		if _, start, err = resolveUnder(root, sub); err != nil {
			return "", err
		}
	}

	var out strings.Builder
	hits, truncated := 0, false

	walkErr := filepath.WalkDir(start, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory is not a reason to abandon the
			// search; the caller gets what is readable.
			return nil //nolint:nilerr // skip what cannot be read, keep walking
		}
		if d.IsDir() {
			if skipSearchDir(d.Name()) && p != start {
				return filepath.SkipDir
			}
			return nil
		}
		if hits >= maxSearchHits {
			truncated = true
			return filepath.SkipAll
		}
		if suffix != "" && !strings.HasSuffix(d.Name(), suffix) {
			return nil
		}
		rel, relErr := filepath.Rel(realRoot, p)
		if relErr != nil {
			return nil
		}
		return searchOneFile(p, rel, re, &out, &hits, &truncated)
	})
	if walkErr != nil {
		return "", walkErr
	}

	if hits == 0 {
		// NOT AN ERROR. "Nothing matches" is a finding, and returning an
		// error would make a model retry a question already answered.
		return fmt.Sprintf("no match for %q", pattern), nil
	}
	if truncated {
		fmt.Fprintf(&out, "... stopped at %d matches; narrow the pattern, or pass path or suffix\n", maxSearchHits)
	}
	return out.String(), nil
}

// searchOneFile scans a single file, appending matches.
func searchOneFile(abs, rel string, re *regexp.Regexp, out *strings.Builder, hits *int, truncated *bool) error {
	f, err := os.Open(abs) // #nosec G304 -- walked from a root proven above
	if err != nil {
		return nil //nolint:nilerr // an unreadable file is skipped, not fatal
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if !re.MatchString(text) {
			continue
		}
		if *hits >= maxSearchHits {
			*truncated = true
			return filepath.SkipAll
		}
		if len(text) > maxSearchLine {
			text = text[:maxSearchLine] + " …"
		}
		fmt.Fprintf(out, "%s:%d: %s\n", rel, line, strings.TrimRight(text, "\r"))
		*hits++
	}
	return nil
}

// skipSearchDir names the directories a content search does not descend. // proven-by: TestSearch_SkipsVendorAndBuildOutput
//
// .git is repository internals and in some clones its config holds a
// credential; the rest are build output and dependencies, which match source
// patterns freely and would exhaust the cap before any source was reached.
//
// proven-by: TestSearch_SkipsTheGitDirectory
// proven-by: TestSearch_SkipsVendorAndBuildOutput
func skipSearchDir(name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", "dist", "bin", ".terraform":
		return true
	}
	return false
}
