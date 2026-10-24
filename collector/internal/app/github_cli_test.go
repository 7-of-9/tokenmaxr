package app

import (
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// The sign-in's closing line words the interval as status does, never as a
// Go duration ("1h0m0s").
func TestPublishEveryTextIsReadable(t *testing.T) {
	a, _, _ := newTestApp(t)
	if got := publishEveryText(a); got != "every hour" {
		t.Fatalf("no config: %q, want every hour", got)
	}
	for minutes, want := range map[int]string{0: "every hour", 30: "every 30 min", 60: "every hour", 90: "every 90 min", 120: "every 2 h"} {
		cfg, err := store.LoadConfig(a.Home)
		if err != nil {
			t.Fatal(err)
		}
		cfg.GitHub = &store.GitHubConfig{PublishEveryMinutes: minutes}
		if err := store.SaveConfig(a.Home, cfg); err != nil {
			t.Fatal(err)
		}
		if got := publishEveryText(a); got != want {
			t.Fatalf("%d minutes: %q, want %q", minutes, got, want)
		}
	}
}
