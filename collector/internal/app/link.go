package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/joincode"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// linkWait is how long a first install waits for the owner's browser.
var linkWait = 15 * time.Minute

// linkJoin gets a join code without anyone typing one (SPEC "Linking a
// machine"): it listens on a loopback port, opens
// <endpoint>/collector/link?port=P&state=S in the browser, and waits for that
// page, signed in as the owner, to send the code from POST /api/link back to
// http://127.0.0.1:P/callback. The state rejects any other caller.
func (a *App) linkJoin(ctx context.Context, endpoint string) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("link: %w", err)
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		ln.Close()
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	codes := make(chan string, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		join := q.Get("join")
		if q.Get("state") != state {
			http.Error(w, "this link is for another collector run", http.StatusBadRequest)
			return
		}
		if _, _, err := joincode.Parse(join); err != nil {
			http.Error(w, "no join code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// Leave the one-use loopback URL for a stable page with no join code.
		http.Redirect(w, r, endpoint+"/collector?linked=1", http.StatusSeeOther)
		select {
		case codes <- join:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		// The handler signals codes before net/http flushes its response. Close
		// would sever that connection and leave Chrome showing a network error
		// even though Install received the code. Drain the handler first.
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	u := endpoint + "/collector/link?" + url.Values{"port": {strconv.Itoa(port)}, "state": {state}}.Encode()
	a.printf("Opening your browser to add this machine (sign in as the owner if asked):\n  %s\n", u)
	open := a.OpenURL
	if open == nil {
		open = tray.Open
	}
	if err := open(u); err != nil {
		a.printf("could not open a browser (%v): open the link above\n", err)
	}

	timer := time.NewTimer(linkWait)
	defer timer.Stop()
	select {
	case join := <-codes:
		return join, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", errors.New("no answer from the browser: run again, or pass --join with a code from `" + buildinfo.Product + " invite`")
	}
}
