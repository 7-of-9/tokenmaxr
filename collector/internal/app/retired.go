package app

import (
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/webcleanup"
)

// Collectors 0.4.14 to 0.4.16 also read the Claude desktop app's local
// Cowork transcripts (native-claude) and claude.ai chat through a Chrome
// extension (web-claude); 0.4.17 removed both (SPEC "Retired Claude
// channels"). What they counted stays in the rollup and on the server.

// retiredSources are the reader names this version no longer has: their
// file cursors and status rows in state.json are dropped, so status and the
// heartbeat list only the sources that are read.
var retiredSources = []string{model.SourceClaudeNative, model.SourceClaudeWeb}

// dropRetiredSources removes the retired readers' state from st, including
// their cursors of a rollup rebuild that 0.4.16 began.
func dropRetiredSources(st *store.State) {
	for _, n := range retiredSources {
		delete(st.Cursors, n)
		delete(st.Sources, n)
		delete(st.GitHub.RebuildCursors, n)
	}
}

// cleanWebCapture removes what 0.4.16's Claude Web capture may have left
// (its Chrome native-messaging registration, extension folder, launcher and
// spool), once per process and best effort: a failure is logged and tried
// again by the next process.
func (a *App) cleanWebCapture() {
	if a.webCleaned {
		return
	}
	a.webCleaned = true
	removed, err := webcleanup.Clean(a.Home)
	for _, r := range removed {
		a.Log.Printf("cleanup: removed the retired Claude Web capture's %s", r)
	}
	if err != nil {
		a.Log.Printf("cleanup: retired Claude Web capture: %v", err)
	}
}
