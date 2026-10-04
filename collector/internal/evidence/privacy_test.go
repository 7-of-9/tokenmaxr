package evidence_test

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
)

// Fixture identities and secrets: none of them may appear in a harvested
// record (only a_/s_ hashes may), and the secrets may not appear anywhere.
const (
	fxAcct    = "9f9f9f9f-1111-4111-8111-aaaaaaaaaaaa"
	fxOrg     = "8e8e8e8e-2222-4222-8222-bbbbbbbbbbbb"
	fxOwner   = "7d7d7d7d-3333-4333-8333-cccccccccccc"
	fxEmail   = "private.person@example.org"
	fxCodexID = "codex-acct-RAWID-4242"
	fxGrokID  = "grok-user-RAWID-4343"
	fxCursor  = "auth0|user_RAWCURSOR4444"
	fxSession = "5c5c5c5c-4444-4444-8444-dddddddddddd"
)

var fxSecrets = []string{
	"sk-ant-FIXTURE-SECRET-1", "FIXTURE-REFRESH-2", "FIXTURE-CODEX-ACCESS-3", "FIXTURE-CODEX-ID-TOKEN-4",
	"FIXTUREKEYPREFIX5", "FIXTURERTPREFIX6", "FIXTURE-GEMINI-OAUTH-7", "FIXTURE-GROK-TOKEN-8", "FIXTURE-LOG-TOKEN-9",
}

func privacyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	p := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	// Claude: a transcript with identity records, snapshots with tokens
	// next to the identity, and the credentials file (never opened).
	write(t, p(".claude", "projects", "proj", fxSession+".jsonl"),
		`{"type":"user","sessionId":"`+fxSession+`","timestamp":"2026-09-20T10:00:00Z","message":{"content":"my token is sk-ant-FIXTURE-SECRET-1 and mail `+fxEmail+`"}}`,
		`{"type":"attachment","sessionId":"`+fxSession+`","uuid":"x1","timestamp":"2026-09-20T10:00:01Z","attachment":{"type":"credential_org","organizationUuid":"`+fxOrg+`"}}`,
		`{"type":"bridge-session","sessionId":"`+fxSession+`","bridgeSessionId":"cse_FIXTURE","ownerAccountUuid":"`+fxOwner+`","ownerOrganizationUuid":"`+fxOrg+`"}`,
	)
	snapshot := `{"primaryApiKey":"sk-ant-FIXTURE-SECRET-1","oauthAccount":{"accountUuid":"` + fxAcct + `","organizationUuid":"` + fxOrg +
		`","emailAddress":"` + fxEmail + `","organizationName":"Fixture Org","accessToken":"FIXTURE-REFRESH-2"}}`
	write(t, p(".claude", "backups", ".claude.json.backup.1790000000000"), snapshot)
	write(t, p(".claude.json.backup"), snapshot)
	write(t, p(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"sk-ant-FIXTURE-SECRET-1","refreshToken":"FIXTURE-REFRESH-2"}}`)
	write(t, p(".claude", "history.jsonl"), `{"display":"/login","timestamp":1789900000000,"project":"/p","sessionId":"`+fxSession+`"}`)
	// Codex: a rollout with the creator id, auth.json (never opened) and a
	// logs database whose other rows hold token-looking text.
	write(t, p(".codex", "sessions", "2026", "09", "28", "rollout-2026-09-28T00-00-00-"+fxSession+".jsonl"),
		`{"timestamp":"2026-09-28T09:00:00Z","type":"session_meta","payload":{"id":"`+fxSession+`","creator_account_id":"`+fxCodexID+`","creator_user_id":"user-RAW","base_instructions":"FIXTURE-CODEX-ACCESS-3"}}`,
		`{"timestamp":"2026-09-28T09:01:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"plan_type":"pro","credits":{"balance":"FIXTURE-CODEX-ID-TOKEN-4"}}}}`,
	)
	write(t, p(".codex", "auth.json"), `{"tokens":{"id_token":"FIXTURE-CODEX-ID-TOKEN-4","access_token":"FIXTURE-CODEX-ACCESS-3","account_id":"`+fxCodexID+`"}}`)
	logsDB(t, p(".codex", "logs_2.sqlite"))
	// Grok: user_info rows carry token prefixes in ctx.
	write(t, p(".grok", "logs", "unified.jsonl"),
		`{"ts":"2026-09-10T05:32:47Z","src":"shell","pid":200,"lvl":"info","msg":"auth init user_info check","ctx":{"user_id":"`+fxGrokID+`","key_prefix":"FIXTUREKEYPREFIX5","rt_prefix":"FIXTURERTPREFIX6"}}`,
		`{"ts":"2026-09-10T05:32:48Z","src":"shell","pid":200,"sid":"`+fxSession+`","lvl":"info","msg":"auth started","ctx":{"method":"cached_token"}}`,
	)
	write(t, p(".grok", "auth.json"), `{"x":{"user_id":"`+fxGrokID+`","email":"`+fxEmail+`","access_token":"FIXTURE-GROK-TOKEN-8"}}`)
	// Gemini: the accounts file, and oauth_creds.json, which is never read.
	write(t, p(".gemini", "google_accounts.json"), `{"active":"`+fxEmail+`","old":[]}`)
	write(t, p(".gemini", "oauth_creds.json"), `{"access_token":"FIXTURE-GEMINI-OAUTH-7","refresh_token":"FIXTURE-GEMINI-OAUTH-7"}`)
	// Cursor: an owner line in the retrieval log.
	write(t, filepath.Join(evidence.CursorLogsDir(home), "20260219T120000", "window1", "exthost", "anysphere.cursor-retrieval", "Cursor Indexing & Retrieval.log"),
		"2026-02-19 12:00:05.000 [info] Starting RepoIndexWatcher for repo: r1, owner: "+fxCursor+" token=FIXTURE-LOG-TOKEN-9")
	return home
}

func logsDB(t *testing.T, path string) {
	t.Helper()
	u := url.URL{Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE logs (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, ts_nanos INTEGER NOT NULL, level TEXT NOT NULL, target TEXT NOT NULL,
			feedback_log_body TEXT, module_path TEXT, file TEXT, line INTEGER, thread_id TEXT, process_uuid TEXT, estimated_bytes INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO logs (ts, ts_nanos, level, target, feedback_log_body, thread_id) VALUES (1790709326, 0, 'INFO', 'codex_login::auth::manager', 'auth: Reloading auth for account ` + fxCodexID + `', NULL)`,
		`INSERT INTO logs (ts, ts_nanos, level, target, feedback_log_body, thread_id) VALUES (1790709327, 0, 'INFO', 'x', 'Request bearer FIXTURE-CODEX-ACCESS-3', '` + fxSession + `')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	hashRe   = regexp.MustCompile(`^a_[0-9a-f]{16}$`)
	streamRe = regexp.MustCompile(`^s_[0-9a-f]{16}$`)
	procRe   = regexp.MustCompile(`^(pid:\d+|log:\d{8}T\d{6})$`)
	planRe   = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// Harvested records hold only hashes, enums, times and plan names: no raw
// id, email, path, prompt text or token, whatever the tools' files hold.
func TestPrivacyRecordsHoldHashesOnly(t *testing.T) {
	home := privacyHome(t)
	marks := map[string]evidence.Mark{}
	res := evidence.Harvest(evidence.Options{Home: home, Hash: hash, Deadline: time.Now().Add(time.Minute)}, marks)
	recs := evidence.Merge(nil, res.Records)
	if len(recs) < 8 {
		t.Fatalf("only %d records harvested", len(recs))
	}
	kinds := map[string]bool{}
	for _, r := range recs {
		kinds[r.Source] = true
		for name, v := range map[string]string{"acct": r.Acct, "org": r.Org} {
			if v != "" && !hashRe.MatchString(v) {
				t.Errorf("%s %s is not a hash: %q", r.Source, name, v)
			}
		}
		if r.Stream != "" && !streamRe.MatchString(r.Stream) {
			t.Errorf("%s stream %q", r.Source, r.Stream)
		}
		if r.Proc != "" && !procRe.MatchString(r.Proc) {
			t.Errorf("%s proc %q", r.Source, r.Proc)
		}
		if r.Plan != "" && !planRe.MatchString(r.Plan) {
			t.Errorf("%s plan %q", r.Source, r.Plan)
		}
		if r.Home != "" {
			t.Errorf("home key %q for the OS home", r.Home)
		}
	}
	for _, src := range []string{evidence.SrcClaudeCredOrg, evidence.SrcClaudeBridge, evidence.SrcClaudeBackup, evidence.SrcClaudeLogin,
		evidence.SrcCodexCreator, evidence.SrcCodexAuthLog, evidence.SrcCodexPlan, evidence.SrcGrokUserInfo, evidence.SrcGrokSession,
		evidence.SrcGeminiAccounts, evidence.SrcCursorRetrieval} {
		if !kinds[src] {
			t.Errorf("no %s record harvested", src)
		}
	}
	// What state.json would hold: the records (and the watermarks' resume
	// context).
	b, _ := json.Marshal(recs)
	state := string(b)
	for _, m := range marks {
		state += m.Aux
	}
	forbidden := append([]string{fxAcct, fxOrg, fxOwner, fxEmail, fxCodexID, fxGrokID, fxCursor, "user_RAWCURSOR", fxSession,
		"Fixture Org", "user-RAW", filepath.ToSlash(home), home}, fxSecrets...)
	for _, s := range forbidden {
		if strings.Contains(state, s) || strings.Contains(strings.ToLower(state), strings.ToLower(s)) {
			t.Errorf("serialized evidence contains %q", s)
		}
	}
	// Labels (local config only) may hold the email, never a secret.
	lb, _ := json.Marshal(res.Labels)
	for _, s := range fxSecrets {
		if strings.Contains(string(lb), s) {
			t.Errorf("labels contain secret %q", s)
		}
	}
}

// The harvesters never name a credential file and never declare a token
// field in their narrow structs.
func TestPrivacyNarrowStructs(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	tag := regexp.MustCompile("json:\"([^\"]*)\"")
	bad := regexp.MustCompile(`(?i)token|secret|password|key_prefix|rt_prefix|refresh|access|api_?key`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(line, "//")
			for _, m := range tag.FindAllStringSubmatch(code, -1) {
				if bad.MatchString(m[1]) {
					t.Errorf("%s:%d decodes field %q", f, i+1, m[1])
				}
			}
			for _, name := range []string{"oauth_creds", ".credentials.json", `"auth.json"`, "accessToken", "id_token"} {
				if strings.Contains(code, name) {
					t.Errorf("%s:%d names %s", f, i+1, name)
				}
			}
		}
	}
}
