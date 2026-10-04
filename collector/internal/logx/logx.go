// Package logx writes collector.log, rotated at 5 MB into collector.log.1.
// Callers must never log prompt text, emails, labels or tokens.
package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const MaxBytes = 5 << 20

type Logger struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
	// Echo, when set, receives a copy of every line (foreground commands).
	Echo io.Writer
}

// Open appends to path, creating its directory.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &Logger{path: path}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

// Discard is a logger that writes nowhere (dry runs, tests).
func Discard() *Logger { return &Logger{} }

func (l *Logger) open() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *Logger) rotate() {
	l.f.Close()
	l.f = nil
	os.Remove(l.path + ".1")
	os.Rename(l.path, l.path+".1")
	if err := l.open(); err != nil {
		l.f = nil
	}
}

// Printf writes one timestamped line.
func (l *Logger) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, args...) + "\n"
	line := time.Now().Format("2006-01-02T15:04:05.000Z07:00") + " " + msg
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Echo != nil {
		io.WriteString(l.Echo, msg)
	}
	if l.f == nil {
		return
	}
	if l.size+int64(len(line)) > MaxBytes {
		l.rotate()
		if l.f == nil {
			return
		}
	}
	n, _ := l.f.WriteString(line)
	l.size += int64(n)
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}
