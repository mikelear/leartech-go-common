package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Kind identifies which forge REST shape a Forge speaks.
type Kind string

const (
	// KindGitHub is api.github.com (and GitHub Enterprise).
	KindGitHub Kind = "github"
	// KindGitea is the Gitea REST API shape, which Forgejo is compatible with.
	// One driver serves both Forgejo and Gitea.
	KindGitea Kind = "gitea"
)

// ParseKind maps a gitKind config value onto a Kind. "forgejo" is accepted as
// an alias for Gitea because Forgejo speaks the Gitea API.
//
// An unknown kind is an error rather than a silent default, so a typo in cluster
// config fails loudly instead of routing a production PR to the wrong forge.
//
// proven-by: TestParseKind
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "github":
		return KindGitHub, nil
	case "gitea", "forgejo":
		return KindGitea, nil
	default:
		return "", fmt.Errorf("%w: unknown forge kind %q (want github|gitea|forgejo)", ErrUnsupported, s)
	}
}

// Sentinel errors. Callers branch with errors.Is; the remote's own message
// travels in the wrapped text. See the package doc for why these are typed.
var (
	// ErrNotFound reports a 404 — the repo, PR, comment or path does not exist.
	ErrNotFound = errors.New("forge: not found")
	// ErrUnauthorized reports a 401 — the token was not accepted. Remedy: re-auth.
	ErrUnauthorized = errors.New("forge: credential not accepted")
	// ErrForbidden reports a 403 — accepted but refused (scope, protection).
	// Says nothing about WHY; the cause is in the wrapped text, from the forge.
	ErrForbidden = errors.New("forge: request refused")
	// ErrConflict reports a 409 — e.g. a PR already open for the head branch.
	ErrConflict = errors.New("forge: conflict")
	// ErrRateLimited reports a 429 / secondary rate limit. Remedy: back off.
	ErrRateLimited = errors.New("forge: rate limited")
	// ErrUnavailable reports a 5xx — a forge outage, not a caller problem.
	ErrUnavailable = errors.New("forge: forge unavailable")
	// ErrUnsupported reports an operation or kind this package does not handle.
	ErrUnsupported = errors.New("forge: unsupported")
)

// Repo is an owner/name pair on a forge.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Comment is one issue/PR comment. Body is the markdown; ID addresses it for
// update. AuthorLogin and a stable identity let a caller find its own sticky
// comment to upsert.
type Comment struct {
	ID          int64
	Body        string
	AuthorLogin string
	HTMLURL     string
}

// PullRequest is the subset of PR fields the estate's consumers use. Number and
// HeadBranch are what the MCP records on an AgentRun; State/Merged drive the
// recorder's merged-vs-closed decision.
type PullRequest struct {
	Number     int
	Title      string
	Body       string
	HeadBranch string
	BaseBranch string
	// HeadSHA and BaseSHA are the tip commit SHAs of the head and base refs.
	HeadSHA string
	BaseSHA string
	State   string // "open" | "closed"
	Merged  bool
	// MergedAt is the merge timestamp (RFC3339) or empty when not merged.
	MergedAt string
	// MergeCommitSHA is the SHA of the merge commit, when merged.
	MergeCommitSHA string
	// Mergeable is the forge's mergeability word (GitHub mergeable_state:
	// "clean"/"dirty"/"blocked"/…). Empty when the forge has not computed it
	// yet, or when the forge does not report one — callers tolerate empty.
	Mergeable string
	// Labels are the PR's label names, in the order the forge returns them.
	Labels  []string
	HTMLURL string
}

// CommitStatus is one commit status on a SHA (the gate view Keeper reports
// through the forge, distinct from CI check-runs). State is the forge's word
// (success/failure/pending/error).
type CommitStatus struct {
	Context     string
	State       string
	TargetURL   string
	Description string
	UpdatedAt   string
}

// OpenPROptions is the input to EnsurePR.
type OpenPROptions struct {
	Repo  Repo
	Head  string
	Base  string
	Title string
	Body  string
}

// ChangedFile is one file in a PR's diff. Status is the forge's word for the
// change (added/modified/removed/renamed); Additions/Deletions are line counts.
type ChangedFile struct {
	Path      string
	Status    string
	Additions int
	Deletions int
}

// PRState filters FindPRByHead. Open matches only open PRs; All matches any state
// (so an already-MERGED PR is visible — the merge-blind "open only" query was a
// real false-negative source for post-merge checks).
type PRState string

const (
	// StateOpen matches only open PRs.
	StateOpen PRState = "open"
	// StateAll matches PRs in any state (open, closed, merged).
	StateAll PRState = "all"
)

// Issue is the subset of an issue the estate's consumers use. An issue and a
// pull request share one number space on both forges, which is why the comment
// methods above take an "issue or PR" number.
type Issue struct {
	Number int
	Title  string
	Body   string
	State  string
	Labels []string
	URL    string
}

// File is one file's contents at a ref.
type File struct {
	Path    string
	Content string
	// Truncated reports a partial body. GitHub truncates above its size limit,
	// so a caller that needs the whole file treats true as a hard stop rather
	// than reading a silent prefix.
	Truncated bool
}

// Commit is one commit's identity.
type Commit struct {
	SHA     string
	Message string
}

// Compare is two refs compared: the commits between them and the files that
// changed. Files carries the same shape ChangedFiles returns, so a consumer
// rendering a PR diff and one rendering a compare read the same type.
type Compare struct {
	AheadBy  int
	BehindBy int
	Commits  []Commit
	Files    []ChangedFile
}

// Forge is the forge-neutral surface. Every method takes a context and returns a
// wrapped sentinel on failure. The read methods are read-only; EnsurePR is
// adopt-or-create (see its own doc).
type Forge interface {
	// Kind reports which REST shape this Forge speaks.
	Kind() Kind

	// ListIssueComments returns every comment on an issue or PR (they share the
	// endpoint on both forges), following pagination.
	ListIssueComments(ctx context.Context, repo Repo, number int) ([]Comment, error)

	// CreateIssueComment posts a new comment and returns it with its assigned ID.
	CreateIssueComment(ctx context.Context, repo Repo, number int, body string) (Comment, error)

	// UpdateIssueComment edits an existing comment by ID.
	UpdateIssueComment(ctx context.Context, repo Repo, commentID int64, body string) (Comment, error)

	// GetPullRequest reads one PR.
	GetPullRequest(ctx context.Context, repo Repo, number int) (PullRequest, error)

	// FindOpenPRByHead returns the open PR whose head is branch, or ErrNotFound.
	FindOpenPRByHead(ctx context.Context, repo Repo, head string) (PullRequest, error)

	// FindPRByHead returns the most-recent PR whose head is branch in the given
	// state (Open or All), or ErrNotFound. StateAll surfaces a MERGED PR, which
	// the open-only FindOpenPRByHead does not return.
	//
	// proven-by: TestFindPRByHead_AllSurfacesMerged
	FindPRByHead(ctx context.Context, repo Repo, head string, state PRState) (PullRequest, error)

	// ListOpenPRs returns every open PR on the repo, newest first, following
	// pagination. This is the "what's in flight right now" view.
	ListOpenPRs(ctx context.Context, repo Repo) ([]PullRequest, error)

	// CommitStatuses returns the commit statuses on sha (the gate view).
	CommitStatuses(ctx context.Context, repo Repo, sha string) ([]CommitStatus, error)

	// ChangedFiles returns the files in a PR's diff, following pagination.
	ChangedFiles(ctx context.Context, repo Repo, number int) ([]ChangedFile, error)

	// PullRequestDiff returns the raw unified diff for a PR.
	PullRequestDiff(ctx context.Context, repo Repo, number int) (string, error)

	// GetIssue reads one issue.
	GetIssue(ctx context.Context, repo Repo, number int) (Issue, error)

	// FileAt reads one file's contents at ref. ref may be a branch, tag or SHA;
	// empty means the repo's default branch.
	FileAt(ctx context.Context, repo Repo, path, ref string) (File, error)

	// HeadSHA returns the commit SHA at ref (a branch, tag or SHA; empty means
	// the default branch). This answers "has this repo moved since the dossier"
	// in one call.
	HeadSHA(ctx context.Context, repo Repo, ref string) (string, error)

	// CompareRefs compares two refs, returning the commits between them and the
	// files that changed.
	CompareRefs(ctx context.Context, repo Repo, base, head string) (Compare, error)

	// EnsurePR adopts the open PR already on the head branch, or creates one. A
	// second call with the same head returns the same PR rather than a
	// duplicate-create error.
	//
	// proven-by: TestEnsurePR_AdoptsExistingOpenPR
	EnsurePR(ctx context.Context, opt OpenPROptions) (PullRequest, error)
}

// ParseKindError returns the ErrUnsupported-wrapped error ParseKind gives for s,
// for callers that already hold a Kind and want the same message on an unknown.
func ParseKindError(s string) error {
	_, err := ParseKind(s)
	return err
}
