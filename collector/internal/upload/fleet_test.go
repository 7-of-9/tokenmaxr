package upload

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The fleet sign-in routes: GET (404 is "none"), PUT and an idempotent
// DELETE, authenticated with the machine token header only.
func TestFleetGitHubClient(t *testing.T) {
	var blob, by string
	var updated time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fleet/github" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(TokenHeader) != "tok" {
			w.WriteHeader(401)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if blob == "" {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":"no fleet sign-in"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"blob": blob, "updatedAt": updated, "by": by})
		case http.MethodPut:
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content type %q", r.Header.Get("Content-Type"))
			}
			var in struct{ Blob string }
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &in)
			blob, by, updated = in.Blob, "m_one", time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
			json.NewEncoder(w).Encode(map[string]any{"updatedAt": updated, "escrowed": true})
		case http.MethodDelete:
			blob = ""
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c := NewClient(srv.URL, "tok", "test")

	if got, err := c.GetFleetGitHub(ctx); got != nil || err != nil {
		t.Fatalf("none: %+v %v", got, err)
	}
	put, err := c.PutFleetGitHub(ctx, "dG14MQ==")
	at := put.UpdatedAt
	if err != nil || !at.Equal(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)) || !put.Escrowed {
		t.Fatalf("put: %+v %v", put, err)
	}
	got, err := c.GetFleetGitHub(ctx)
	if err != nil || got == nil || got.Blob != "dG14MQ==" || got.By != "m_one" || !got.UpdatedAt.Equal(at) {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := c.DeleteFleetGitHub(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFleetGitHub(ctx); err != nil {
		t.Fatalf("deleting again: %v", err)
	}
	if got, _ := c.GetFleetGitHub(ctx); got != nil {
		t.Fatal("still shared after delete")
	}

	bad := NewClient(srv.URL, "revoked", "test")
	if _, err := bad.GetFleetGitHub(ctx); Code(err) != 401 {
		t.Fatalf("revoked token: %v", err)
	}
	if _, err := bad.PutFleetGitHub(ctx, "dG14MQ=="); Code(err) != 401 {
		t.Fatalf("revoked put: %v", err)
	}
	if err := bad.DeleteFleetGitHub(ctx); Code(err) != 401 {
		t.Fatalf("revoked delete: %v", err)
	}
}
