package aigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// MinAPILevel is the gateway wire level this client needs.
//
// Compared as an INTEGER rather than a version string. Releases in this estate
// are per-cluster streams -- gcp may be at v0.0.5 while az is at v0.0.7 with
// neither ahead -- so version numbers are not orderable across clusters. Two
// such gaps were read as drift in one session and both readings were wrong.
//
// Raise this only when the client genuinely fails below it. A feature
// that degrades visibly does NOT need a raise: `ship-proven models` already says
// when a gateway does not report suppliers, which is more useful than
// refusing to run.
//
// proven-by: TestVerdict_TooOldIsUnreachableAtTheCurrentMinimum
const MinAPILevel = 1

// VersionResponse is GET /version, which needs no credential.
type VersionResponse struct {
	Version  string `json:"version"`
	APILevel int    `json:"api_level"`
}

// Version reports the gateway's build and wire level.
//
// Deliberately callable WITHOUT a token: the whole point is to tell "this
// gateway is too old" apart from "this credential is wrong", which are
// indistinguishable once a call is behind auth. Client.do refuses an empty
// credential before sending, so this does not go through it.
//
// proven-by: TestVersion_IsAskedWithoutSendingACredential
// proven-by: TestVersion_AGatewayWithoutTheEndpointReportsLevelZero
func (c *Client) Version(ctx context.Context) (VersionResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/version", nil)
	if err != nil {
		return VersionResponse{}, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return VersionResponse{}, err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusNotFound {
		// A gateway predating /version. Level 0 is "did not say", which is
		// not the same as "level 1" -- the caller has to be able to tell a
		// silent gateway from one that answered.
		return VersionResponse{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return VersionResponse{}, fmt.Errorf(
			"gateway: GET /version returned %d", res.StatusCode)
	}
	var out VersionResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return VersionResponse{}, fmt.Errorf(
			"gateway: GET /version returned 200 with a body that is not the "+
				"expected JSON: %w", err)
	}
	return out, nil
}
