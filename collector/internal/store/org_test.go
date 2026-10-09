package store

import (
	"testing"
	"time"
)

// The first organisation a home's login is seen in is not a switch; a change
// is, from when it was seen, and stays until the next change.
func TestNoteOrg(t *testing.T) {
	st := NewState()
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	if got := st.NoteOrg("|anthropic", "a_team", t0); !got.IsZero() {
		t.Fatalf("first org: %v", got)
	}
	if got := st.NoteOrg("|anthropic", "a_team", t0.Add(time.Hour)); !got.IsZero() {
		t.Fatalf("same org: %v", got)
	}
	t1 := t0.Add(2 * time.Hour)
	if got := st.NoteOrg("|anthropic", "a_me", t1); !got.Equal(t1) {
		t.Fatalf("switch: %v", got)
	}
	if got := st.NoteOrg("|anthropic", "a_me", t1.Add(time.Hour)); !got.Equal(t1) {
		t.Fatalf("after the switch: %v", got)
	}
	if got := st.NoteOrg("/wsl/home|anthropic", "a_me", t1); !got.IsZero() {
		t.Fatalf("another home: %v", got)
	}
	if got := st.NoteOrg("|anthropic", "", t1); !got.IsZero() || st.Orgs["|anthropic"].Org != "a_me" {
		t.Fatalf("no org: %v %+v", got, st.Orgs)
	}
}
