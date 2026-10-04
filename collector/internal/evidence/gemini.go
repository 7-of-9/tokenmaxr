package evidence

import (
	"path/filepath"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/sources/gemini"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

// gemini harvests google_accounts.json (M1): the active account at the
// file's last write, and whether any other account was ever active ("old"
// is empty when this was the only login). oauth_creds.json is never opened.
func (h *harvester) gemini() bool {
	path := filepath.Join(gemini.Dir(h.o.Home), "google_accounts.json")
	st, changed := h.changed(path)
	if !changed {
		return true
	}
	b, err := jsonl.ReadFile(path)
	if err != nil {
		return true
	}
	h.marks[path] = Mark{Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), Off: st.Size()}
	h.res.Files++
	var f struct {
		Active string           `json:"active"`
		Old    []jsonRawIgnored `json:"old"`
	}
	if !jsonl.Decode(b, &f) || strings.TrimSpace(f.Active) == "" {
		return true
	}
	a := h.hash(gemini.ProviderName, f.Active)
	h.label(a, f.Active)
	h.out(Record{Provider: gemini.ProviderName, Kind: KindSample, Source: SrcGeminiAccounts, Q: QStrong, Acct: a,
		Sole: len(f.Old) == 0, TS: st.ModTime().UTC()})
	return true
}

// jsonRawIgnored counts list entries without decoding them.
type jsonRawIgnored struct{}

func (*jsonRawIgnored) UnmarshalJSON([]byte) error { return nil }
