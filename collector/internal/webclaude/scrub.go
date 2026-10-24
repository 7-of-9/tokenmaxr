package webclaude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Prompt text in the spool. The host writes a turn's prompt text only while
// chat prompt capture allows it, and the collector deletes it again once its
// cursor has passed the line, that is once the prompt record is in the
// outbox (SPEC "Claude chat"). Scrubbing rewrites each text-bearing line in
// place as the same record without its text, padded with spaces to the same
// length: every offset (the collector's cursors) stays valid, and the file
// keeps its size and modification time, so the scan does not read it again.

// ScrubText deletes the prompt text of every complete line of the spool file
// at path that ends at or before upto. It returns how many lines it changed
// and whether a line with text remains before upto (a line it could not
// rewrite). The caller holds Lock.
func ScrubText(path string, upto int64) (n int, left bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return 0, false, err
	}
	upto = min(upto, st.Size())
	br := bufio.NewReaderSize(io.LimitReader(f, upto), 64<<10)
	type patch struct {
		at int64
		b  []byte
	}
	var patches []patch
	var off int64
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			if b, ok, has := scrubLine(line[:len(line)-1]); ok {
				patches = append(patches, patch{off, b})
			} else if has {
				left = true
			}
		}
		off += int64(len(line))
		if rerr != nil {
			break
		}
	}
	for _, p := range patches {
		if _, werr := f.WriteAt(p.b, p.at); werr != nil {
			err = werr
			break
		}
		n++
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if n > 0 {
		// The same size and modification time: an unchanged file to the scan.
		_ = os.Chtimes(path, st.ModTime(), st.ModTime())
	}
	return n, left || err != nil, err
}

var textKey = []byte(`"text"`)

// scrubLine returns line without its prompt text, padded to the same length
// (ok), or has when the line carries text it cannot rewrite.
func scrubLine(line []byte) (out []byte, ok, has bool) {
	if !bytes.Contains(line, textKey) {
		return nil, false, false
	}
	r, err := Decode(line)
	if err != nil || r.Text == nil {
		return nil, false, false
	}
	r.Text = nil
	b, err := json.Marshal(r)
	if err != nil || len(b) > len(line) {
		return nil, false, true
	}
	return append(b, bytes.Repeat([]byte{' '}, len(line)-len(b))...), true, false
}

// ScrubAll deletes the prompt text of every spool line (uninstall).
func ScrubAll(dir string) error {
	l, err := Lock(dir)
	if err != nil {
		return err
	}
	defer l.Release()
	files, err := SpoolFiles(dir)
	for _, p := range files {
		if _, _, serr := ScrubText(p, 1<<62); serr != nil {
			err = errors.Join(err, serr)
		}
	}
	return err
}
