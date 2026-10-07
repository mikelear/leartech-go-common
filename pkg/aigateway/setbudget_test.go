package aigateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// SetBudget PUTs an absolute cap to /admin/v1/keys/{keyid}/budget and decodes
// what the gateway recorded. This is the keys_admin path that can RAISE a cap.
func TestSetBudget_PutsAbsoluteCap(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody SetBudgetRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(SetBudgetResponse{KeyID: "kZ", Tenant: "t7", BudgetMicros: 200000000})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", srv.Client())
	got, err := c.SetBudget(context.Background(), "kZ", 200000000)
	if err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/admin/v1/keys/kZ/budget" {
		t.Fatalf("called %s %s", gotMethod, gotPath)
	}
	if gotBody.BudgetMicros != 200000000 {
		t.Fatalf("sent budget=%d", gotBody.BudgetMicros)
	}
	if got.Tenant != "t7" || got.BudgetMicros != 200000000 {
		t.Fatalf("decoded %+v", got)
	}
}
