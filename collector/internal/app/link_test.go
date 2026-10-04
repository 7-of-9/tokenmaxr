package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/joincode"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// A first install with no --join opens the link page and enrolls with the
// code that page sends back to the loopback callback.
func TestInstallLinksThroughTheBrowser(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	k := bytes.Repeat([]byte{0x5a}, 32)
	var opened string
	callback := make(chan error, 1)
	a.OpenURL = func(u string) error {
		opened = u
		p, err := url.Parse(u)
		if err != nil {
			return err
		}
		q := p.Query()
		// The wrong state is refused; the right one carries the code.
		go func() {
			base := "http://127.0.0.1:" + q.Get("port") + "/callback?"
			if res, err := http.Get(base + url.Values{"state": {"nope"}, "join": {"D0M1-x"}}.Encode()); err == nil {
				res.Body.Close()
				if res.StatusCode != http.StatusBadRequest {
					t.Errorf("wrong state answered %d", res.StatusCode)
				}
			}
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			res, err := client.Get(base + url.Values{"state": {q.Get("state")}, "join": {joincode.Format("first", k)}}.Encode())
			if err != nil {
				callback <- err
				return
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err == nil && (res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != srv.URL+"/collector?linked=1") {
				err = fmt.Errorf("callback did not redirect to the stable confirmation page: %d", res.StatusCode)
			}
			if err == nil && (res.Header.Get("Referrer-Policy") != "no-referrer" || len(body) == 0) {
				err = fmt.Errorf("callback missing privacy header or complete body")
			}
			callback <- err
		}()
		return nil
	}
	err := a.Install(context.Background(), InstallOptions{Endpoint: srv.URL, Label: "box", Yes: true, NoAutostart: true})
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.HasPrefix(opened, srv.URL+"/collector/link?") {
		t.Fatalf("opened %q", opened)
	}
	sec, _ := store.LoadSecrets(a.Home)
	if sec.MachineID != "m_first" || !bytes.Equal(sec.Key(), k) || api.fingerprint != model.KFingerprint(k) {
		t.Fatalf("enrolled %q, key from the link %v", sec.MachineID, bytes.Equal(sec.Key(), k))
	}
	if err := <-callback; err != nil {
		t.Fatalf("browser callback failed although enrollment succeeded: %v", err)
	}
	if !a.Installed() {
		t.Fatal("Installed() after install")
	}
}

func TestLinkGivesUp(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.OpenURL = func(string) error { return nil }
	old := linkWait
	linkWait = 50 * time.Millisecond
	defer func() { linkWait = old }()
	if _, err := a.linkJoin(context.Background(), "https://example.invalid"); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("err = %v", err)
	}
}
