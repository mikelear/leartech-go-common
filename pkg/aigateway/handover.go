package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
)

// The handover is the conversational cousin of TransformSession.
//
// TransformSession rewrites a saved session's bytes for a new model and never
// consults either model — it works when the checkpoint already holds everything
// the target needs. A HANDOVER is for when it does not: a session that refers to
// local files ("I wrote the summary to NOTES.md") carries the sentence but not
// the file, so a bare transform leaves the target believing in context it cannot
// read. The handover lets the two models DISCUSS the work — the target
// summarises it back and names anything it is missing, the source confirms or
// corrects — until the source agrees the target has a faithful grasp.
//
// THE FILE BOUNDARY, which the client is the reason for. The gateway never reads
// the operator's disk. When the target asks for a file, the response is
// PhaseNeedFiles with the paths; the CLER (the ship-proven shell / agent) reads
// those files locally and calls SessionHandover again with their contents in
// Files. The gateway stitches them into the dialogue. So a handover is a LOOP on
// the caller's side: call, and while the phase is need_files, upload the named
// files and call again — accumulating Files across calls — until the phase is
// agreed (or cap_hit).
//
// AS RAW JSON for the same reason TransformSession is: this package does not
// depend on agentloop, so the caller marshals its Checkpoint to json.RawMessage
// and unmarshals the returned one back.

// Handover phase constants mirror the gateway's. A caller branches on these
// rather than comparing strings inline.
const (
	// HandoverPhaseAgreed: the source confirmed the target's understanding is
	// faithful. Checkpoint is set and ready to save to a new resume slot.
	HandoverPhaseAgreed = "agreed"
	// HandoverPhaseNeedFiles: the target asked for local files the gateway does
	// not have. RequestedFiles names them; the caller uploads and calls again.
	// This is the ONLY phase that pauses for the caller.
	HandoverPhaseNeedFiles = "need_files"
	// HandoverPhaseCapHit: the dialogue reached MaxRounds without agreement.
	// Checkpoint is set only when AllowCapHit was requested.
	HandoverPhaseCapHit = "cap_hit"
)

// HandoverFile is one local file the caller has uploaded because the target
// asked for it on a previous call. The gateway replays it into the dialogue; the
// bytes are read by the CALLER, never by the gateway.
type HandoverFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SessionHandoverRequest asks the gateway to run the two-model handover dialogue.
type SessionHandoverRequest struct {
	// TargetModel is the model the session will be handed to.
	TargetModel string `json:"target_model"`
	// SourceModel is the model the session is handed FROM. Unlike transform it is
	// required: the dialogue needs a model to speak for the source and confirm.
	SourceModel string `json:"source_model"`
	// Checkpoint is the saved session, as raw JSON (the agentloop.Checkpoint
	// shape), exactly as the shell wrote it.
	Checkpoint json.RawMessage `json:"checkpoint"`
	// Files carries any local files the caller uploaded in response to an earlier
	// need_files reply. Empty on the first call, accumulated across the loop.
	Files []HandoverFile `json:"files,omitempty"`
	// MaxRounds caps the dialogue; 0 lets the gateway apply its default. A
	// parameter so a caller can raise it for a large handover.
	MaxRounds int `json:"max_rounds,omitempty"`
	// AllowCapHit decides what a cap_hit MEANS. When false (default), reaching
	// the cap without agreement is a 409 — the handover did not establish
	// understanding. When true, the gateway transforms the session anyway and
	// returns it flagged cap_hit: the "one sign-off is acceptable" path.
	AllowCapHit bool `json:"allow_cap_hit,omitempty"`
	// ReplyHeadroomTokens is passed through to the final transform, mirroring
	// TransformSessionRequest.
	ReplyHeadroomTokens int `json:"reply_headroom_tokens,omitempty"`
}

// SessionHandoverResponse is one step of the handover: a settled session, a
// request for files, or a cap-hit report — Phase says which.
type SessionHandoverResponse struct {
	Object string `json:"object"` // "session.handover"
	// Phase is agreed, need_files, or cap_hit (the Handover* constants above).
	Phase string `json:"phase"`
	// Checkpoint is the resumable session for the target, set only when Phase is
	// agreed (or cap_hit with AllowCapHit). Raw JSON, same as transform.
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	// TargetModel echoes what the session was handed TO.
	TargetModel string `json:"target_model"`
	// RequestedFiles names the local files the caller must upload and call back
	// with, set only when Phase is need_files.
	RequestedFiles []string `json:"requested_files,omitempty"`
	// Handover is the audit trail of the dialogue so far.
	Handover HandoverReport `json:"handover"`
	// Transform reports what the final rewrite changed, set alongside Checkpoint.
	Transform *TransformReport `json:"transform,omitempty"`
}

// HandoverReport is the auditable record of the dialogue.
type HandoverReport struct {
	Phase          string          `json:"phase"`
	Rounds         []HandoverRound `json:"rounds"`
	RequestedFiles []string        `json:"requested_files,omitempty"`
	RoundsUsed     int             `json:"rounds_used"`
	CapHit         bool            `json:"cap_hit"`
	Notes          []string        `json:"notes"`
}

// HandoverRound is one turn in the dialogue: who spoke and what they said.
type HandoverRound struct {
	Speaker string `json:"speaker"`
	Role    string `json:"role"` // "target" or "source"
	Content string `json:"content"`
}

// SessionHandover runs one step of the two-model handover dialogue.
//
// POSTs /v1/sessions/handover. The credential may be a JWT or an sk-lt- virtual
// key with chat scope — the same credential a chat turn and a transform use,
// because a handover drives chat turns on two models.
//
// The CALLER drives the loop: inspect resp.Phase, and while it is
// HandoverPhaseNeedFiles, read resp.RequestedFiles from local disk, append them
// to req.Files, and call again. A 409 (surfaced as an APIError) means the models
// did not agree within the cap and AllowCapHit was not set.
//
// proven-by: TestSessionHandover_PostsToTheEndpoint
// proven-by: TestSessionHandover_CarriesUploadedFiles
// proven-by: TestSessionHandover_DecodesANeedFilesStep
func (c *Client) SessionHandover(ctx context.Context, req SessionHandoverRequest) (SessionHandoverResponse, error) {
	var r SessionHandoverResponse
	if err := c.do(ctx, http.MethodPost, "/v1/sessions/handover", req, &r); err != nil {
		return SessionHandoverResponse{}, err
	}
	return r, nil
}
