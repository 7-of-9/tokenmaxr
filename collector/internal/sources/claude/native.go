package claude

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

// Native reads the desktop app's local Cowork records. Desktop Code sessions
// already stored in ~/.claude/projects belong to the existing Code reader.
// Cloud-only chats/tasks are not inferred from account quota or app activity.
type Native struct{}

func NewNative() sources.Source            { return &Native{} }
func (*Native) Name() string               { return model.SourceClaudeNative }
func (*Native) Provider() string           { return model.ProviderAnthropic }
func (*Native) PV() int                    { return pv }
func (*Native) Prepare(*sources.Env) error { return nil }

func NativeDir(home string) string {
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = filepath.Join(home, "Library", "Application Support", "Claude")
	case "windows":
		dir = filepath.Join(home, "AppData", "Roaming", "Claude")
	default:
		dir = filepath.Join(home, ".config", "Claude")
	}
	return filepath.Join(dir, "local-agent-mode-sessions")
}

var nativeUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (*Native) Files(env *sources.Env) ([]string, error) {
	root := NativeDir(env.Home)
	var files []string
	// Only account/org/task directories and their SDK transcript tree. Do not
	// inspect cookies, browser databases, plugins, user-selected folders or VMs.
	accounts, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if !account.IsDir() || !nativeUUID.MatchString(account.Name()) {
			continue
		}
		orgs, err := os.ReadDir(filepath.Join(root, account.Name()))
		if err != nil {
			return nil, err
		}
		for _, org := range orgs {
			if !org.IsDir() || !nativeUUID.MatchString(org.Name()) {
				continue
			}
			tasks, err := os.ReadDir(filepath.Join(root, account.Name(), org.Name()))
			if err != nil {
				return nil, err
			}
			for _, task := range tasks {
				if !task.IsDir() {
					continue
				}
				dir := filepath.Join(root, account.Name(), org.Name(), task.Name())
				projects := filepath.Join(dir, ".claude", "projects")
				var transcripts []string
				err = filepath.WalkDir(projects, func(path string, entry fs.DirEntry, err error) error {
					if os.IsNotExist(err) && path == projects {
						return fs.SkipAll
					}
					if err != nil {
						return err
					}
					if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") {
						transcripts = append(transcripts, path)
					}
					return nil
				})
				if err != nil {
					return nil, err
				}
				// audit.jsonl mirrors the SDK transcript, including its message IDs.
				// Prefer the original timestamps and session IDs whenever available.
				if len(transcripts) > 0 {
					files = append(files, transcripts...)
					continue
				}
				audit := filepath.Join(dir, "audit.jsonl")
				if st, err := os.Lstat(audit); err == nil && st.Mode().IsRegular() {
					files = append(files, audit)
				}
			}
		}
	}
	return files, nil
}

func (*Native) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	e := &jsonl.Emitter{Env: env, Provider: model.ProviderAnthropic, Source: model.SourceClaudeNative, PV: pv}
	rel, err := filepath.Rel(NativeDir(env.Home), path)
	if err == nil {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) >= 4 && nativeUUID.MatchString(parts[0]) && nativeUUID.MatchString(parts[1]) {
			e.Hint = jsonl.HintFor(env, model.ProviderAnthropic, sources.HintAccount, parts[0], time.Time{})
		}
	}
	return parseTranscriptWith(env, path, cur, e)
}
