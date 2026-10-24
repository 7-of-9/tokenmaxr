// Package nativehost is tokenmaxr's Chrome native-messaging host for
// Claude Web capture (SPEC "Claude chat"). Chrome starts it for the pinned
// extension only (allowed_origins in the host manifest) and talks to it over
// stdin/stdout: each message is a 32-bit length in native byte order
// followed by that many bytes of JSON. There is no network listener.
//
// The host validates every record strictly, drops prompt text unless both
// the collector's prompt capture and the Claude chat prompt setting allow it
// for the record's kind (webclaude.Settings.TextAllowed), appends to the collector-owned spool
// (internal/webclaude) and replies with the settings the extension needs.
// It never logs ids, text or values: counts only.
package nativehost

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

// Message size caps. Chrome caps host-to-browser messages at 1 MB and
// browser-to-host ones at 64 MB (MaxFrame). The service worker sends at most
// 4 MB per message; a larger message, up to MaxFrame, is read, discarded and
// answered with TooLarge, so the worker splits the batch and the queue never
// stalls. A length past MaxFrame is broken framing: the host exits.
const (
	MaxIn      = 8 << 20
	MaxFrame   = 64 << 20
	MaxOut     = 1 << 20
	MaxRecords = 500
	maxLog     = 512 << 10
)

// Options configure one host process.
type Options struct {
	// Dir is the web-claude state directory (paths.WebClaude).
	Dir string
	// Origin is the caller Chrome passed as the first argument
	// ("chrome-extension://<id>/"); ExtensionID is the pinned id.
	Origin      string
	ExtensionID string
	// Prompts reports whether the collector's prompt capture is on. Chat
	// text also needs the web-claude prompt setting.
	Prompts func() bool
	Version string
	In      io.Reader
	Out     io.Writer
	Now     func() time.Time
}

// Request is a message from the extension.
type Request struct {
	Type    string            `json:"type"`
	Ext     string            `json:"ext,omitempty"`
	Records []json.RawMessage `json:"records,omitempty"`
}

// Reply is the host's answer: always the current settings, plus the
// outcome for records.
type Reply struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version"`
	// Prompts: the text of live turns is kept; SweepPrompts: that of swept
	// turns too. Both need the collector's prompt capture and the chat
	// prompt setting.
	Prompts       bool   `json:"prompts"`
	SweepPrompts  bool   `json:"sweepPrompts"`
	Sweeper       bool   `json:"sweeper"`
	InferApps     bool   `json:"inferApps"`
	SettleMinutes int    `json:"settleMinutes"`
	T0            string `json:"t0"`
	// TooLarge: the message was over MaxIn and was not read.
	TooLarge bool     `json:"tooLarge,omitempty"`
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors,omitempty"`
}

// ErrOrigin means a caller other than the pinned extension started the host.
var ErrOrigin = errors.New("native host: caller is not the tokenmaxr extension")

// Run serves messages until stdin closes.
func Run(o Options) error {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Prompts == nil {
		o.Prompts = func() bool { return false }
	}
	want := "chrome-extension://" + o.ExtensionID + "/"
	if o.ExtensionID == "" || strings.TrimSpace(o.Origin) != want {
		logf(o.Dir, o.Now(), "rejected a caller that is not the pinned extension")
		return ErrOrigin
	}
	for {
		msg, err := ReadMessage(o.In, MaxIn)
		if errors.Is(err, io.EOF) {
			return nil
		}
		var big *TooLargeError
		if errors.As(err, &big) && big.N <= MaxFrame {
			if _, err := io.CopyN(io.Discard, o.In, big.N); err != nil {
				logf(o.Dir, o.Now(), "bad message framing")
				return io.ErrUnexpectedEOF
			}
			logf(o.Dir, o.Now(), fmt.Sprintf("skipped a message of %d bytes", big.N))
			if err := WriteMessage(o.Out, Reply{Error: "message too large", TooLarge: true, Version: o.Version}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			logf(o.Dir, o.Now(), "bad message framing")
			return err
		}
		rep := handle(&o, msg)
		if err := WriteMessage(o.Out, rep); err != nil {
			return err
		}
	}
}

func handle(o *Options, msg []byte) Reply {
	now := o.Now().UTC()
	var req Request
	if err := json.Unmarshal(msg, &req); err != nil {
		return Reply{Error: "message is not JSON", Version: o.Version}
	}
	l, err := webclaude.Lock(o.Dir)
	if err != nil {
		return Reply{Error: "state is busy", Version: o.Version}
	}
	defer l.Release()
	t0, err := webclaude.Enrol(o.Dir, now)
	if err != nil {
		return Reply{Error: "cannot write state", Version: o.Version}
	}
	set := webclaude.LoadSettings(o.Dir)
	capture := o.Prompts()
	text := func(kind string) bool { return capture && set.TextAllowed(kind) }
	rep := Reply{OK: true, Version: o.Version, Prompts: text(webclaude.KindLive), SweepPrompts: text(webclaude.KindSweep),
		Sweeper: set.Sweeper, InferApps: set.Infer(),
		SettleMinutes: int(set.Settle() / time.Minute), T0: t0.Format(time.RFC3339)}
	hello, _ := webclaude.LoadHello(o.Dir)
	hello.Ext, hello.Origin, hello.ConnectedAt = clip(req.Ext, 16), o.ExtensionID, now
	if day := now.Format("2006-01-02"); hello.Day != day {
		hello.Day, hello.RecordsToday = day, 0
	}
	switch req.Type {
	case "hello":
	case "records":
		if len(req.Records) > MaxRecords {
			rep.OK, rep.Error = false, fmt.Sprintf("at most %d records per message", MaxRecords)
			break
		}
		var lines [][]byte
		var claims []string
		var pending []webclaude.Pending
		for _, raw := range req.Records {
			r, err := webclaude.DecodeHost(raw)
			if err == nil {
				err = r.Validate(now, true)
			}
			if err != nil {
				rep.Rejected++
				if len(rep.Errors) < 5 {
					rep.Errors = append(rep.Errors, err.Error())
				}
				continue
			}
			if r.Kind == webclaude.KindClaim {
				// A pending turn: no spool line (see webclaude.KindClaim).
				pending = append(pending, webclaude.Pending{ID: webclaude.UsageID(r.Assistant), Platform: r.Platform})
				rep.Accepted++
				continue
			}
			if r.Text != nil {
				if !text(r.Kind) {
					r.Text = nil
				} else {
					t := jsonl.PromptText(*r.Text)
					r.Text = &t
				}
			}
			r.At = now.Format(time.RFC3339Nano)
			b, err := encodeLine(&r)
			if err != nil {
				rep.Rejected++
				if len(rep.Errors) < 5 {
					rep.Errors = append(rep.Errors, err.Error())
				}
				continue
			}
			lines = append(lines, b)
			if r.Kind == webclaude.KindLive {
				claims = append(claims, webclaude.UsageID(r.Assistant))
			}
			rep.Accepted++
		}
		err := webclaude.Append(o.Dir, now, lines, claims)
		if err == nil {
			err = webclaude.AppendPending(o.Dir, pending)
		}
		if err != nil {
			rep.OK, rep.Error, rep.Accepted = false, "cannot write the spool", 0
			break
		}
		if len(lines) > 0 {
			hello.LastRecordAt = now
		}
		hello.RecordsToday += len(lines)
		hello.Records += int64(len(lines))
		hello.Rejected += int64(rep.Rejected)
		webclaude.Prune(o.Dir, now)
		logf(o.Dir, now, fmt.Sprintf("records accepted=%d rejected=%d ext=%s", rep.Accepted, rep.Rejected, clip(req.Ext, 16)))
	default:
		rep.OK, rep.Error = false, "unknown message type"
	}
	_ = webclaude.SaveHello(o.Dir, hello)
	return rep
}

// encodeLine encodes r as a spool line that webclaude.Decode accepts. JSON
// escaping can make a line with text near model.MaxPromptBytes longer than
// webclaude.MaxRecordBytes; the text is then cut (with the collector's
// truncation marker) until the line fits, and dropped as a last resort, so
// the turn's numbers are never lost and no line is written or claimed that
// the collector would skip.
func encodeLine(r *webclaude.Record) ([]byte, error) {
	for try := 0; ; try++ {
		b, err := webclaude.Encode(r)
		if err != nil {
			return nil, errors.New("record cannot be encoded")
		}
		if len(b) <= webclaude.MaxRecordBytes {
			return b, nil
		}
		if r.Text == nil {
			return nil, errors.New("record too large")
		}
		if try >= 3 {
			r.Text = nil
			continue
		}
		// Each byte of text takes at least one byte of the line, so cutting
		// the excess (plus room for the marker) is enough at once.
		cut := len(*r.Text) - (len(b) - webclaude.MaxRecordBytes) - 256
		t := jsonl.Truncate(*r.Text, max(cut, 0))
		r.Text = &t
	}
}

func clip(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= n {
			break
		}
		if r >= '0' && r <= '9' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// logf appends one line to host.log (counts only), keeping it small.
func logf(dir string, now time.Time, msg string) {
	if dir == "" {
		return
	}
	p := webclaude.LogPath(dir)
	if st, err := os.Stat(p); err == nil && st.Size() > maxLog {
		_ = os.Rename(p, p+".old")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s %s\n", now.UTC().Format(time.RFC3339), msg)
	f.Close()
}

// TooLargeError is a message longer than ReadMessage's limit; its N bytes
// have not been read.
type TooLargeError struct{ N int64 }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("native message of %d bytes is out of bounds", e.N)
}

// ReadMessage reads one length-prefixed message.
func ReadMessage(r io.Reader, max int) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.NativeEndian, &n); err != nil {
		return nil, err // io.EOF: Chrome closed the pipe
	}
	if n == 0 {
		return nil, fmt.Errorf("native message of 0 bytes is out of bounds")
	}
	if int64(n) > int64(max) {
		return nil, &TooLargeError{N: int64(n)}
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return b, nil
}

// WriteMessage writes one length-prefixed JSON message.
func WriteMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxOut {
		return fmt.Errorf("native reply of %d bytes is too large", len(b))
	}
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(append(hdr[:], b...)); err != nil {
		return err
	}
	if f, ok := w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	return nil
}
