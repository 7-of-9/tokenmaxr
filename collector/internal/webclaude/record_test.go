package webclaude

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func valid() Record {
	return Record{V: 1, Kind: KindLive, Platform: PlatformWeb,
		Org: "11111111-2222-4333-8444-555555555555", Conv: "73c9aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Human: "a0a0aaaa-0000-4000-8000-000000000001", Assistant: "a0a0aaaa-0000-4000-8000-000000000002",
		TS: "2026-10-23T07:39:09.250000Z", PromptTS: "2026-10-23T07:39:07.125000Z", Model: "claude-fable-5-1",
		ChatPlatform: "CLAUDE_AI", Stop: "end_turn", Calls: 1, ThinkingMs: 256,
		Est: Estimate{In: 12, Out: 3, Algo: Algorithm}, Ext: "0.1.0"}
}

func TestValidateAcceptsACapturedTurn(t *testing.T) {
	r := valid()
	if err := r.Validate(now, true); err != nil {
		t.Fatal(err)
	}
	if got := r.PromptTime(); got.Format(time.RFC3339) != "2026-10-23T07:39:07Z" {
		t.Fatalf("PromptTime %v", got)
	}
}

func TestValidateRejectsOutOfBoundsWithoutQuotingValues(t *testing.T) {
	secret := "99999999-9999-4999-8999-99999999999x"
	cases := map[string]func(*Record){
		"v":         func(r *Record) { r.V = 2 },
		"kind":      func(r *Record) { r.Kind = "import" },
		"platform":  func(r *Record) { r.Platform = "android" },
		"org":       func(r *Record) { r.Org = secret },
		"assistant": func(r *Record) { r.Assistant = r.Human },
		"model":     func(r *Record) { r.Model = "claude<script>" },
		"calls":     func(r *Record) { r.Calls = 0 },
		"est.in":    func(r *Record) { r.Est.In = MaxIn + 1 },
		"est.out":   func(r *Record) { r.Est.Out = -1 },
		"est.algo":  func(r *Record) { r.Est.Algo = "" },
		"ts":        func(r *Record) { r.TS = "2026-11-23T00:00:00Z" },
		"promptTs":  func(r *Record) { r.PromptTS = "2026-10-23T08:00:00Z" },
		"at":        func(r *Record) { r.At = "2026-10-23T08:00:00Z" },
		"stop":      func(r *Record) { r.Stop = "End Turn" },
	}
	for field, mutate := range cases {
		r := valid()
		mutate(&r)
		err := r.Validate(now, true)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: err = %v", field, err)
		}
		if err != nil && strings.Contains(err.Error(), "9999") {
			t.Errorf("%s: error quotes the value: %v", field, err)
		}
	}
}

func TestDecodeIsStrict(t *testing.T) {
	if _, err := Decode([]byte(`{"v":1,"kind":"live","cookie":"x"}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := Decode([]byte(`{"v":1} {"v":1}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	if _, err := Decode(make([]byte, MaxRecordBytes+1)); err == nil {
		t.Fatal("oversized record accepted")
	}
}

func TestCanonicalIDsIgnoreUUIDCase(t *testing.T) {
	if UsageID("A0A0AAAA-0000-4000-8000-000000000002") != UsageID("a0a0aaaa-0000-4000-8000-000000000002") {
		t.Fatal("usage id depends on uuid case")
	}
	if UsageKey("A") != "msg:a" || PromptKey("B") != "prompt:b" {
		t.Fatal("keys")
	}
}

func TestSettingsDefaults(t *testing.T) {
	var s Settings
	if !s.Infer() || s.Settle() != DefaultSettle {
		t.Fatal("defaults")
	}
	off := false
	s = Settings{InferApps: &off, SettleMinutes: -1}
	if s.Infer() || s.Settle() != 0 {
		t.Fatal("overrides")
	}
}

func TestSpoolClaimsAndEnrolment(t *testing.T) {
	dir := t.TempDir()
	t0, err := Enrol(dir, now)
	if err != nil || !t0.Equal(now) {
		t.Fatal(t0, err)
	}
	if again, _ := Enrol(dir, now.Add(time.Hour)); !again.Equal(t0) {
		t.Fatal("T0 moved")
	}
	if err := Append(dir, now, [][]byte{[]byte(`{"a":1}`)}, []string{UsageID("x")}); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, now.Add(-50*24*time.Hour), [][]byte{[]byte(`{"a":0}`)}, nil); err != nil {
		t.Fatal(err)
	}
	files, _ := SpoolFiles(dir)
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	Prune(dir, now)
	files, _ = SpoolFiles(dir)
	if len(files) != 1 || !strings.HasSuffix(files[0], "2026-10-23.jsonl") {
		t.Fatalf("after prune %v", files)
	}
	if c := Claims(dir); !c[UsageID("x")] || len(c) != 1 {
		t.Fatalf("claims %v", c)
	}
	if err := Append(dir, now, nil, []string{UsageID("y")}); err != nil {
		t.Fatal(err)
	}
	if c := Claims(dir); !c[UsageID("y")] {
		t.Fatal("claims cache did not refresh")
	}
}
