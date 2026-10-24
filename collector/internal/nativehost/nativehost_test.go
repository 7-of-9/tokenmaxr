package nativehost

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

const extID = "cbbaflndpdfcnifplodaobkmbcghdaoc"

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func frame(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], uint32(len(b)))
	return append(hdr[:], b...)
}

func replies(t *testing.T, out []byte) []Reply {
	t.Helper()
	r := bytes.NewReader(out)
	var all []Reply
	for {
		b, err := ReadMessage(r, MaxOut)
		if errors.Is(err, io.EOF) {
			return all
		}
		if err != nil {
			t.Fatal(err)
		}
		var rep Reply
		if err := json.Unmarshal(b, &rep); err != nil {
			t.Fatal(err)
		}
		all = append(all, rep)
	}
}

func record(text string) map[string]any {
	r := map[string]any{"v": 1, "kind": "live", "platform": "web_claude_ai",
		"org": "11111111-2222-4333-8444-555555555555", "conv": "73c9aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"human": "a0a0aaaa-0000-4000-8000-000000000001", "assistant": "a0a0aaaa-0000-4000-8000-000000000002",
		"ts": "2026-10-23T07:39:09.250000Z", "promptTs": "2026-10-23T07:39:07.125000Z", "model": "claude-fable-5-1",
		"chatPlatform": "CLAUDE_AI", "stop": "end_turn", "calls": 1, "est": map[string]any{"in": 12, "out": 3, "algo": "chars-v1"}}
	if text != "" {
		r["text"] = text
	}
	return r
}

func run(t *testing.T, dir string, prompts bool, msgs ...any) ([]Reply, error) {
	t.Helper()
	var in bytes.Buffer
	for _, m := range msgs {
		in.Write(frame(t, m))
	}
	var out bytes.Buffer
	err := Run(Options{Dir: dir, Origin: "chrome-extension://" + extID + "/", ExtensionID: extID,
		Prompts: func() bool { return prompts }, Version: "dev", In: &in, Out: &out, Now: func() time.Time { return now }})
	return replies(t, out.Bytes()), err
}

func spool(t *testing.T, dir string) []webclaude.Record {
	t.Helper()
	files, _ := webclaude.SpoolFiles(dir)
	var out []webclaude.Record
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == "" {
				continue
			}
			r, err := webclaude.Decode([]byte(l))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
	}
	return out
}

func TestFramingRoundTrip(t *testing.T) {
	var b bytes.Buffer
	if err := WriteMessage(&b, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&b, MaxIn)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := ReadMessage(&b, MaxIn); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream: %v", err)
	}
}

func TestRejectsOversizedAndTruncatedFrames(t *testing.T) {
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], MaxIn+1)
	if _, err := ReadMessage(bytes.NewReader(hdr[:]), MaxIn); err == nil {
		t.Fatal("oversized frame accepted")
	}
	binary.NativeEndian.PutUint32(hdr[:], 10)
	if _, err := ReadMessage(bytes.NewReader(append(hdr[:], 'x')), MaxIn); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

func TestOnlyThePinnedExtension(t *testing.T) {
	dir := t.TempDir()
	err := Run(Options{Dir: dir, Origin: "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/", ExtensionID: extID,
		In: bytes.NewReader(frame(t, map[string]string{"type": "hello"})), Out: io.Discard, Now: func() time.Time { return now }})
	if !errors.Is(err, ErrOrigin) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "enrolled.json")); err == nil {
		t.Fatal("a foreign caller enrolled the machine")
	}
}

func TestHelloEnrolsAndReportsSettings(t *testing.T) {
	dir := t.TempDir()
	if err := webclaude.SaveSettings(dir, webclaude.Settings{Sweeper: true, SettleMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	reps, err := run(t, dir, true, map[string]string{"type": "hello", "ext": "0.1.0"})
	if err != nil || len(reps) != 1 {
		t.Fatal(reps, err)
	}
	r := reps[0]
	// The collector's prompt capture alone does not turn on chat text.
	if !r.OK || r.Prompts || r.SweepPrompts || !r.Sweeper || !r.InferApps || r.SettleMinutes != 30 || r.T0 != "2026-10-23T12:00:00Z" {
		t.Fatalf("%+v", r)
	}
	h, ok := webclaude.LoadHello(dir)
	if !ok || h.Ext != "0.1.0" || h.Origin != extID {
		t.Fatalf("hello %+v", h)
	}
}

func TestRecordsAreValidatedSpooledAndClaimed(t *testing.T) {
	dir := t.TempDir()
	bad := record("")
	bad["cookie"] = "sessionKey=x"
	sweep := record("")
	sweep["kind"] = "sweep"
	sweep["assistant"] = "a0a0aaaa-0000-4000-8000-000000000003"
	reps, err := run(t, dir, false, map[string]any{"type": "records", "ext": "0.1.0", "records": []any{record("private prompt"), bad, sweep}})
	if err != nil {
		t.Fatal(err)
	}
	r := reps[0]
	if !r.OK || r.Accepted != 2 || r.Rejected != 1 {
		t.Fatalf("%+v", r)
	}
	recs := spool(t, dir)
	if len(recs) != 2 {
		t.Fatalf("spool %d", len(recs))
	}
	for _, rec := range recs {
		if rec.Text != nil {
			t.Fatal("prompt text spooled while prompt capture is off")
		}
		if rec.At == "" || rec.Validate(now, false) != nil {
			t.Fatalf("spooled record %+v", rec)
		}
	}
	claims := webclaude.Claims(dir)
	if len(claims) != 1 || !claims[webclaude.UsageID("a0a0aaaa-0000-4000-8000-000000000002")] {
		t.Fatalf("claims %v (only live records claim)", claims)
	}
	log, _ := os.ReadFile(webclaude.LogPath(dir))
	for _, secret := range []string{"11111111", "73c9aaaa", "a0a0aaaa", "private", "sessionKey"} {
		if bytes.Contains(log, []byte(secret)) {
			t.Fatalf("log contains %q: %s", secret, log)
		}
	}
	if !bytes.Contains(log, []byte("accepted=2 rejected=1")) {
		t.Fatalf("log %s", log)
	}
	h, _ := webclaude.LoadHello(dir)
	if h.RecordsToday != 2 || h.Records != 2 || h.Rejected != 1 {
		t.Fatalf("hello %+v", h)
	}
}

// Chat prompt text needs the collector's prompt capture and the chat prompt
// setting: "live" keeps live turns' text only, "all" swept turns' too.
func TestPromptTextKeptOnlyWhenCaptureIsOn(t *testing.T) {
	sweep := func(text string) map[string]any {
		r := record(text)
		r["kind"] = "sweep"
		r["assistant"] = "a0a0aaaa-0000-4000-8000-000000000003"
		return r
	}
	for _, tc := range []struct {
		capture            bool
		mode               string
		wantLive, wantSwep bool
	}{
		{true, "", false, false},
		{true, "off", false, false},
		{true, "live", true, false},
		{true, "all", true, true},
		{false, "all", false, false},
	} {
		dir := t.TempDir()
		if err := webclaude.SaveSettings(dir, webclaude.Settings{Prompts: tc.mode}); err != nil {
			t.Fatal(err)
		}
		reps, err := run(t, dir, tc.capture, map[string]any{"type": "records", "records": []any{record("  hello  "), sweep("old")}})
		if err != nil {
			t.Fatal(err)
		}
		if reps[0].Prompts != tc.wantLive || reps[0].SweepPrompts != tc.wantSwep {
			t.Fatalf("%+v: reply %+v", tc, reps[0])
		}
		for _, rec := range spool(t, dir) {
			want, text := tc.wantLive, "hello"
			if rec.Kind == webclaude.KindSweep {
				want, text = tc.wantSwep, "old"
			}
			if got := rec.Text != nil; got != want || got && *rec.Text != text {
				t.Fatalf("%+v: %s text %v", tc, rec.Kind, rec.Text)
			}
		}
	}
}

func TestTooManyRecordsAndUnknownTypes(t *testing.T) {
	dir := t.TempDir()
	many := make([]any, MaxRecords+1)
	for i := range many {
		many[i] = record("")
	}
	reps, err := run(t, dir, false, map[string]any{"type": "records", "records": many}, map[string]string{"type": "delete"})
	if err != nil {
		t.Fatal(err)
	}
	if reps[0].OK || reps[1].OK || len(spool(t, dir)) != 0 {
		t.Fatalf("%+v", reps)
	}
}

// A message over MaxIn is skipped with a TooLarge reply, and the host serves
// the next one: the extension splits its batch instead of stalling.
func TestOversizedMessageIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	var in bytes.Buffer
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], MaxIn+10)
	in.Write(hdr[:])
	in.Write(bytes.Repeat([]byte{'x'}, MaxIn+10))
	in.Write(frame(t, map[string]any{"type": "records", "records": []any{record("")}}))
	var out bytes.Buffer
	err := Run(Options{Dir: dir, Origin: "chrome-extension://" + extID + "/", ExtensionID: extID,
		Version: "dev", In: &in, Out: &out, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	reps := replies(t, out.Bytes())
	if len(reps) != 2 || reps[0].OK || !reps[0].TooLarge || !reps[1].OK || reps[1].Accepted != 1 {
		t.Fatalf("%+v", reps)
	}
	// Past Chrome's own 64 MB limit the framing is broken: the host stops.
	in.Reset()
	binary.NativeEndian.PutUint32(hdr[:], MaxFrame+1)
	in.Write(hdr[:])
	if err := Run(Options{Dir: dir, Origin: "chrome-extension://" + extID + "/", ExtensionID: extID,
		In: &in, Out: &out, Now: func() time.Time { return now }}); err == nil {
		t.Fatal("broken framing accepted")
	}
}

// A claim (the stream ended; the live record follows) becomes a pending
// turn, not a spool line and not a live claim.
func TestClaimsArePendingTurns(t *testing.T) {
	dir := t.TempDir()
	claim := map[string]any{"v": 1, "kind": "claim", "platform": "desktop_app",
		"org": "11111111-2222-4333-8444-555555555555", "conv": "73c9aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"assistant": "a0a0aaaa-0000-4000-8000-000000000009", "ts": "2026-10-23T07:39:09Z", "calls": 1,
		"est": map[string]any{"in": 0, "out": 0, "algo": "chars-v1"}}
	reps, err := run(t, dir, false, map[string]any{"type": "records", "records": []any{claim}})
	if err != nil || !reps[0].OK || reps[0].Accepted != 1 {
		t.Fatalf("%+v %v", reps, err)
	}
	if len(spool(t, dir)) != 0 || len(webclaude.Claims(dir)) != 0 {
		t.Fatal("a claim was spooled or claimed")
	}
	p := webclaude.PendingClaims(dir)
	if plat, ok := p[webclaude.UsageID("a0a0aaaa-0000-4000-8000-000000000009")]; !ok || plat != "desktop_app" {
		t.Fatalf("pending %v", p)
	}
	if h, _ := webclaude.LoadHello(dir); h.Records != 0 {
		t.Fatalf("a claim counted as a record: %+v", h)
	}
}

// frameJS frames v as the extension does: JSON.stringify leaves < > & as
// they are.
func frameJS(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], uint32(len(b)))
	return append(hdr[:], b...)
}

// A large prompt never costs the turn: whatever its JSON escaping, the host
// accepts the record and writes a spool line the collector can read, cutting
// the text to fit when it must.
func TestLargePromptKeepsTurn(t *testing.T) {
	cases := map[string]string{
		// < > & : json.Marshal would make each 6 bytes.
		"html": strings.Repeat("<td>x</td>", 15*1024),
		// Tabs, quotes and newlines up to the extension's 256 KiB cap.
		"code": strings.Repeat("\tfmt.Println(\"x\")\n", 250*1024/18),
		// Control characters: 6 bytes each in JSON.
		"control": strings.Repeat("\x01", 200*1024),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := webclaude.SaveSettings(dir, webclaude.Settings{Prompts: webclaude.PromptsLive}); err != nil {
				t.Fatal(err)
			}
			var in bytes.Buffer
			in.Write(frameJS(t, map[string]any{"type": "records", "records": []any{record(text)}}))
			var out bytes.Buffer
			if err := Run(Options{Dir: dir, Origin: "chrome-extension://" + extID + "/", ExtensionID: extID,
				Prompts: func() bool { return true }, Version: "dev", In: &in, Out: &out, Now: func() time.Time { return now }}); err != nil {
				t.Fatal(err)
			}
			reps := replies(t, out.Bytes())
			if len(reps) != 1 || reps[0].Accepted != 1 || reps[0].Rejected != 0 {
				t.Fatalf("reply %+v", reps)
			}
			recs := spool(t, dir) // fails on a line Decode refuses
			if len(recs) != 1 || recs[0].Est.In != 12 {
				t.Fatalf("spool %+v", recs)
			}
			if name == "html" && (recs[0].Text == nil || *recs[0].Text != text) {
				t.Fatal("HTML text that fits should be kept whole")
			}
			if recs[0].Text != nil && len(*recs[0].Text) > len(text)+64 {
				t.Fatal("text grew")
			}
		})
	}
}
