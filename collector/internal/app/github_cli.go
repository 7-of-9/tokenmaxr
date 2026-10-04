package app

import (
	"context"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/termfmt"
)

// cliLoginUI prints the GitHub sign-in steps on the terminal.
type cliLoginUI struct{ a *App }

func (u cliLoginUI) Code(code, url string) {
	u.a.printf("\nSign in to GitHub: your browser is opening %s\nEnter this code there:\n\n    %s\n\n", url, code)
}

func (u cliLoginUI) Step(text, url string) {
	u.a.printf("\nNext, on GitHub: %s\n  %s\n(the browser opens it; this continues by itself once it is done)\n", text, url)
}

func (u cliLoginUI) Progress(text string) { u.a.printf("%s\n", text) }

// GitHubLoginCLI runs the GitHub sign-in on the terminal and nudges a
// running desktop app to publish straight away.
func (a *App) GitHubLoginCLI(ctx context.Context, label string) error {
	res, err := a.GitHubLogin(ctx, cliLoginUI{a}, label)
	if err != nil {
		return err
	}
	a.printf("\nDone. This machine publishes to %s as %q.\n", res.Repo, res.Label)
	if res.PagesURL != "" {
		a.printf("Your dashboard: %s (the first build takes a few minutes)\n", res.PagesURL)
	}
	if res.Rehash {
		a.printf("This machine joined your existing fleet: its history is being re-read.\n")
	}
	if instance.Running(a.Home) {
		_ = instance.Send(a.Home, instance.Show)
		a.printf("The app is publishing now; afterwards every %s.\n", publishEveryText(a))
	} else {
		a.printf("Publishing starts with the next tick (`" + buildinfo.Product + " sync-now` to publish now).\n")
	}
	a.printf("Rename this machine there: " + buildinfo.Product + " github login --label NAME. Stop: " + buildinfo.Product + " github logout\n")
	return nil
}

func publishEveryText(a *App) string {
	cfg, err := store.LoadConfig(a.Home)
	if err != nil || cfg.GitHub == nil {
		return "30 minutes"
	}
	return cfg.GitHub.PublishEvery().String()
}

// GitHubLogoutCLI stops publishing to GitHub on this machine.
func (a *App) GitHubLogoutCLI() error {
	cfg, _ := store.LoadConfig(a.Home)
	if err := a.GitHubLogout(); err != nil {
		return err
	}
	if cfg.GitHub != nil {
		a.printf("This machine no longer publishes to %s. What it published stays there; revoke the App's access at https://github.com/settings/installations if you want.\n", cfg.GitHub.Repo)
	} else {
		a.printf("This machine was not publishing to GitHub.\n")
	}
	return nil
}

// GitHubStatusCLI prints the GitHub publisher's state.
func (a *App) GitHubStatusCLI() error {
	cfg, _ := store.LoadConfig(a.Home)
	sec, _ := store.LoadSecrets(a.Home)
	st, _ := store.LoadState(a.Home)
	a.githubRows(termfmt.New(a.Out), cfg, sec, st, a.Now())
	return nil
}
