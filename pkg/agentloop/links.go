package agentloop

import "regexp"

// prLink matches a GitHub pull request or issue URL.
//
// NARROW ON PURPOSE. Every URL in every tool result would bury the one
// worth clicking: reading a document of links would print dozens and
// the operator stops looking. A pull request or an issue is the thing
// someone is being asked to go and open.
//
// proven-by: TestPullRequestLinks_FindsAPullRequestURL
// proven-by: TestPullRequestLinks_IgnoresOrdinaryURLs
var prLink = regexp.MustCompile(
	`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/(?:pull|issues)/\d+`)

// PullRequestLinks returns the distinct PR and issue URLs in a string,
// in the order they appear.
//
// Tool output reaches the operator as a character count. // proven-by: TestRunner_APullRequestInToolOutputReachesTheScreen
// The model can open a pull request and the caller says "↳ 214 chars".
//
// proven-by: TestPullRequestLinks_DeduplicatesAndKeepsOrder
func PullRequestLinks(s string) []string {
	found := prLink.FindAllString(s, -1)
	if len(found) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(found))
	out := make([]string, 0, len(found))
	for _, u := range found {
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}
