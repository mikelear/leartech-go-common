package agentloop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tool is something the loop can run locally.
//
// Definition is the OpenAI function-tool shape, so it goes into a chat
// request unchanged. Translating between a local shape and the wire shape
// would be a second place for the two to disagree.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Run         func(args json.RawMessage) (string, error)

	// Effect is what this tool does to the machine. Required: a tool
	// registers with an effect or does not register.
	// proven-by: TestRegistry_RefusesAToolThatDeclaresNoEffect
	Effect Effect

	// Preview renders what an operator is being asked to approve.
	//
	// THE TOOL DECIDES, NOT THE PROMPT. A script shows as its text, a
	// write shows as a diff, and a caller that guessed from the raw
	// arguments would render both as escaped JSON on one line —
	// unreadable exactly when reading matters. Optional: without it the
	// arguments are shown as they are.
	//
	// proven-by: TestPreviewOf_UsesTheToolsOwnRenderer
	// proven-by: TestPreviewOf_FallsBackToTheArguments
	Preview func(args json.RawMessage) string
}

// Registry is the set of tools a shell offers, by qualified name.
//
// QUALIFIED NAMES, DECIDED BY MIKE 2026-09-19: local__read_file, not
// read_file. Three sources will feed this list — local tools, MCP servers
// and the gateway's own — and the alternative to qualification is a
// precedence rule, which resolves collisions SILENTLY. The losing tool
// simply is not there.
//
// The stronger reason is that the qualified name is what gets shown at the
// moment of approval. Someone approving a call needs to know whether it
// reads a local file, hits our MCP host, or spends money at a supplier.
// "read_file" does not say; "local__read_file" does.
type Registry struct {
	tools map[string]Tool
	order []string

	policy   *Policy
	approver Approver
}

// NewRegistry returns a registry that permits reading and nothing else.
//
// THE DEFAULT IS THE SAFE ONE, so a caller that forgets to set a policy
// gets a read-only shell rather than an ungoverned one.
//
// proven-by: TestRegistry_TheDefaultPolicyIsReadOnly
func NewRegistry() *Registry {
	return &Registry{
		tools:    map[string]Tool{},
		policy:   ReadOnly(),
		approver: DenyAll{},
	}
}

// Govern replaces the policy and the approver.
//
// proven-by: TestRegistry_AGovernedWriteIsRefusedUntilApproved
func (r *Registry) Govern(p *Policy, a Approver) {
	if p != nil {
		r.policy = p
	}
	if a != nil {
		r.approver = a
	}
}

// ErrDuplicateTool reports two tools claiming one name.
//
// REFUSED RATHER THAN OVERWRITTEN. Last-registration-wins is a precedence
// rule by another name, and the tool that loses vanishes with no signal.
var ErrDuplicateTool = errors.New("agentloop: two tools claim the same qualified name")

// ErrNoEffect reports a tool that did not say what it does.
//
// proven-by: TestRegistry_RefusesAToolThatDeclaresNoEffect
var ErrNoEffect = errors.New("agentloop: a tool must declare an Effect")

// Add registers a tool, refusing a name already taken.
//
// proven-by: TestRegistry_RefusesADuplicateRatherThanShadowing
func (r *Registry) Add(t Tool) error {
	if t.Effect == "" {
		// Refused, not defaulted: an undeclared tool would fall under a
		// rule written for something else. // proven-by: TestRegistry_RefusesAToolThatDeclaresNoEffect
		return fmt.Errorf("%w: %s", ErrNoEffect, t.Name)
	}
	if _, taken := r.tools[t.Name]; taken {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// PreviewOf renders a pending call for approval.
//
// proven-by: TestPreviewOf_UsesTheToolsOwnRenderer
// proven-by: TestPreviewOf_FallsBackToTheArguments
func (r *Registry) PreviewOf(name string, args json.RawMessage) string {
	t, ok := r.tools[name]
	if !ok {
		return strings.TrimSpace(string(args))
	}
	if t.Preview == nil {
		return strings.TrimSpace(string(args))
	}
	return t.Preview(args)
}

// Names returns the qualified names in registration order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Run executes a tool by qualified name.
//
// proven-by: TestRegistry_AnUnknownToolIsReportedToTheModel
// proven-by: TestRegistry_AFailureReachesTheModelAsText
//
// An unknown tool is an error the MODEL sees, not a crash and not silence.
func (r *Registry) Run(name string, args json.RawMessage) (string, bool) {
	t, ok := r.tools[name]
	if !ok {
		return fmt.Sprintf("no tool named %q. Available: %s",
			name, strings.Join(r.Names(), ", ")), false
	}

	// EVERY CALL PASSES THROUGH THE GATE, and the gate is here rather
	// than inside each tool. When the rooting lived in the tools, glob
	// remembered to skip .git and read_file did not — a gate a tool can
	// forget to call is a gate with a hole per tool.
	//
	// proven-by: TestRegistry_EveryCallPassesTheGate
	// proven-by: TestRegistry_AGovernedWriteIsRefusedUntilApproved
	// proven-by: TestRegistry_AnAskWithNobodyToAskIsRefused
	req := Request{Tool: t.Name, Effect: t.Effect, Args: args}
	switch verdict, reason := r.policy.Decide(req); verdict {
	case Deny:
		return refusal(req, reason), false
	case Ask:
		if !r.approver.Approve(req, reason) {
			return refusal(req, "declined"), false
		}
	case Allow:
	}

	out, err := t.Run(args)
	if err != nil {
		// THE OUTPUT SURVIVES THE FAILURE. Discarding it threw away
		// exactly the part worth reading: bash returns its stderr
		// alongside a non-zero exit, and an MCP tool returns the
		// server's own message alongside IsError. Keeping only the
		// wrapper turned "deepwiki: repository not indexed" into
		// "read_wiki_contents reported an error", and the model — told
		// nothing it could act on — simply retried.
		//
		// proven-by: TestRegistry_AFailingToolKeepsItsOutput
		if out != "" {
			return out + "\n\n" + err.Error(), false
		}
		return err.Error(), false
	}
	return out, true
}

// ReadFile is the one local tool, deliberately narrow.
//
// proven-by: TestReadFile_RefusesToEscapeTheRoot
// proven-by: TestReadFile_RefusesASymlinkPointingOut
// proven-by: TestReadFile_APrefixSiblingIsNotInside
// proven-by: TestReadFile_ReadsAFileUnderTheRoot
//
// ROOTED, AND THE ROOT IS NOT A SUGGESTION — the MODEL chooses the path
// argument, so an unrooted read is one that can reach ~/.ssh/id_rsa.
func ReadFile(root string) Tool {
	return Tool{
		Name:   "local__read_file",
		Effect: Reads,
		Description: "Read a UTF-8 text file from the working directory. " +
			"Paths are relative to it and cannot escape it.",
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{"path":{"type":"string","description":"path relative to the working directory"}},
		  "required":["path"]
		}`),
		Run: func(args json.RawMessage) (string, error) {
			var in struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("arguments are not valid JSON: %w", err)
			}
			if in.Path == "" {
				return "", errors.New("path is required")
			}
			return readUnder(root, in.Path)
		},
	}
}

// resolveUnder resolves a path and refuses anything outside root.
//
// EvalSymlinks on the RESOLVED path, not the cleaned one: filepath.Clean
// removes "../" textually and would accept a symlink whose target escapes.
// The root is resolved too, so a symlinked working directory does not make
// every path look external.
//
// SHARED BY EVERY TOOL THAT TAKES A PATH. The model chooses the argument,
// so each tool that re-derived this would be a fresh chance to get it
// wrong — and the failure is silent until something reads ~/.ssh/id_rsa.
//
// proven-by: TestListDir_RefusesToEscapeTheRoot
// proven-by: TestListDir_RefusesASymlinkPointingOut
// proven-by: TestReadFile_RefusesToEscapeTheRoot
// proven-by: TestReadFile_RefusesASymlinkPointingOut
func resolveUnder(root, rel string) (realRoot, resolved string, err error) {
	realRoot, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("working directory is unreadable: %w", err)
	}
	resolved, err = filepath.EvalSymlinks(filepath.Join(realRoot, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("no such file: %s", rel)
		}
		return "", "", err
	}
	if !under(realRoot, resolved) {
		return "", "", fmt.Errorf("refusing to open %s: it resolves outside "+
			"the working directory", rel)
	}
	if insideGitDir(realRoot, resolved) {
		return "", "", fmt.Errorf("refusing to open %s: .git is repository "+
			"internals rather than the project, and in some clones its config "+
			"holds a credential. Ask for what you actually need", rel)
	}
	return realRoot, resolved, nil
}

// insideGitDir reports whether a resolved path is in a .git directory.
//
// GLOB ALREADY SKIPPED .git AND THE READ TOOLS DID NOT, which is the
// inconsistency this removes. Skipping it there was about noise; refusing
// it here is about a clone whose .git/config carries
// https://user:token@github.com/... — the model picks the path, so the
// tool has to be the thing that says no.
//
// Observed 2026-09-20: asked "what's new", glm read .git/HEAD,
// .git/refs/heads/main and .git/logs/HEAD in one turn, reconstructing git
// log from internals because it had no git tool.
//
// proven-by: TestReadFile_RefusesGitInternals
// proven-by: TestListDir_RefusesTheGitDirectory
// proven-by: TestResolveUnder_AFileMerelyNamedGitIsFine
func insideGitDir(root, resolved string) bool {
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	return false
}

func readUnder(root, rel string) (string, error) {
	_, resolved, err := resolveUnder(root, rel)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", rel)
	}
	const maxBytes = 256 * 1024
	if info.Size() > maxBytes {
		// A cap, because the result goes into the next prompt and is paid
		// for by the token. Truncating silently would hand the model a
		// partial file it believes is whole.
		return "", fmt.Errorf("%s is %d bytes; the limit is %d because the "+
			"contents are sent to the model", rel, info.Size(), maxBytes)
	}
	b, err := os.ReadFile(resolved) // #nosec G304 -- resolved and proven under root above
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// under reports whether path is root or inside it.
//
// Compared on separator boundaries: a prefix test alone would accept
// /work-secrets as being inside /work.
func under(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// ListDir is what the model asked for out loud.
//
// glm, given a working directory and no filename, guessed nine paths —
// ROUTER.md, docs/router.md, ARCHITECTURE.md and so on — and exhausted its
// tool budget without reaching the file. Measured 2026-09-19; the model
// named the missing capability in its own answer.
//
// proven-by: TestListDir_ListsEntriesAndMarksDirectories
// proven-by: TestListDir_RefusesToEscapeTheRoot
// proven-by: TestListDir_RefusesASymlinkPointingOut
// proven-by: TestListDir_AFileIsNotADirectory
// proven-by: TestListDir_SaysWhenItTruncated
func ListDir(root string) Tool {
	return Tool{
		Name:   "local__list_dir",
		Effect: Reads,
		Description: "List the files and directories in one directory of the " +
			"working directory. Does not recurse. Paths are relative to the " +
			"working directory and cannot escape it.",
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{"path":{"type":"string","description":"directory relative to the working directory; \"\" or \".\" for the root"}}
		}`),
		Run: func(args json.RawMessage) (string, error) {
			var in struct {
				Path string `json:"path"`
			}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &in); err != nil {
					return "", fmt.Errorf("arguments are not valid JSON: %w", err)
				}
			}
			if in.Path == "" {
				in.Path = "."
			}
			return listUnder(root, in.Path)
		},
	}
}

// maxEntries bounds one listing.
//
// The result is sent to the model and paid for by the token, so a
// directory of ten thousand files is a bill rather than an answer.
const maxEntries = 300

func listUnder(root, rel string) (string, error) {
	_, resolved, err := resolveUnder(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is a file, not a directory; read it with local__read_file", rel)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		// Not an empty string. A model handed "" learns nothing and will
		// usually try the same call again.
		return fmt.Sprintf("%s is empty", rel), nil
	}

	var b strings.Builder
	shown := entries
	if len(shown) > maxEntries {
		shown = shown[:maxEntries]
	}
	for _, e := range shown {
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
			continue
		}
		if fi, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "%s (%d bytes)\n", e.Name(), fi.Size())
			continue
		}
		fmt.Fprintf(&b, "%s\n", e.Name())
	}
	if len(entries) > maxEntries {
		// SAID, NOT SILENT. A truncated listing the model believes is
		// complete is worse than no listing: it concludes a file is absent.
		fmt.Fprintf(&b, "\n(%d of %d entries shown; the rest were not listed)\n",
			maxEntries, len(entries))
	}
	return b.String(), nil
}

// Glob finds files by pattern, which is the other half of aiming.
//
// MATCHING RULE, STATED BECAUSE IT IS A CHOICE: a pattern containing no
// separator is matched against each BASE name at any depth, so "*.md"
// finds docs/notes.md. A pattern containing one is matched against the
// path relative to the root, so "docs/*.md" does not. filepath.Match has
// no "**", and inventing one here would be a second syntax to learn.
//
// proven-by: TestGlob_FindsByBaseNameAtAnyDepth
// proven-by: TestGlob_APatternWithASeparatorMatchesTheRelativePath
// proven-by: TestGlob_RefusesToEscapeTheRoot
// proven-by: TestGlob_SkipsTheGitDirectory
// proven-by: TestGlob_SaysWhenNothingMatched
// proven-by: TestGlob_SaysWhenItTruncated
func Glob(root string) Tool {
	return Tool{
		Name:   "local__glob",
		Effect: Reads,
		Description: "Find files in the working directory by shell pattern. " +
			"A pattern without a slash matches file names at any depth " +
			"(\"*.md\"); one with a slash matches the path relative to the " +
			"working directory (\"docs/*.md\").",
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{"pattern":{"type":"string","description":"shell pattern, e.g. *.go or internal/*.go"}},
		  "required":["pattern"]
		}`),
		Run: func(args json.RawMessage) (string, error) {
			var in struct {
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("arguments are not valid JSON: %w", err)
			}
			if in.Pattern == "" {
				return "", errors.New("pattern is required")
			}
			return globUnder(root, in.Pattern)
		},
	}
}

const maxMatches = 200

func globUnder(root, pattern string) (string, error) {
	if strings.Contains(pattern, "..") {
		// Refused on the pattern itself so the answer says why. The walk
		// below is what actually contains it. // proven-by: TestGlob_RefusesToEscapeTheRoot
		return "", fmt.Errorf("refusing the pattern %q: it tries to leave the "+
			"working directory", pattern)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("working directory is unreadable: %w", err)
	}
	if _, err := filepath.Match(pattern, "probe"); err != nil {
		return "", fmt.Errorf("pattern %q is not valid: %w", pattern, err)
	}

	byBase := !strings.Contains(pattern, string(filepath.Separator))
	var found []string
	truncated := false

	err = filepath.WalkDir(realRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory is not a reason to abandon the
			// search; the caller gets what is readable.
			return nil //nolint:nilerr // skip what cannot be read, keep walking
		}
		if d.IsDir() {
			// .git holds thousands of files that no one is looking for,
			// and it would exhaust the cap before reaching real ones.
			if d.Name() == ".git" && p != realRoot {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(realRoot, p)
		if relErr != nil {
			return nil
		}
		subject := rel
		if byBase {
			subject = d.Name()
		}
		if ok, _ := filepath.Match(pattern, subject); !ok {
			return nil
		}
		if len(found) >= maxMatches {
			truncated = true
			return filepath.SkipAll
		}
		found = append(found, rel)
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(found) == 0 {
		// An explicit answer. An empty result is indistinguishable from a
		// broken tool, and a model told nothing tries again.
		return fmt.Sprintf("nothing matched %q under the working directory", pattern), nil
	}
	out := strings.Join(found, "\n")
	if truncated {
		out += fmt.Sprintf("\n\n(stopped at %d matches; there are more)", maxMatches)
	}
	return out, nil
}
