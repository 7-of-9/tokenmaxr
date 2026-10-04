// Command limits prints the usage meters this machine's tools have
// already written, as {"items":[LimitSnapshot]}. It does not enroll, upload,
// or write state. The local dev server serves this at GET /api/limits.
package main

import (
	"encoding/json"
	"os"

	"github.com/7-of-9/tokenmaxr/collector/internal/limits"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		os.Exit(1)
	}
	items := limits.Collect(&sources.Env{Home: home})
	if items == nil {
		items = []model.LimitSnapshot{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"items": items}); err != nil {
		os.Exit(1)
	}
}
