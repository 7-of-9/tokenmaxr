package main

import (
	"fmt"
	"io"
	"os"

	"github.com/7-of-9/tokenmaxr/collector/extension"
	"github.com/7-of-9/tokenmaxr/collector/internal/nativehost"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/webcapture"
)

// nativeHost runs the Chrome native-messaging host for Claude Web capture
// (internal/nativehost). Stdout carries only framed messages; errors go to
// stderr, which Chrome discards.
func nativeHost(homeFlag, origin string, stdout, stderr io.Writer) int {
	home, err := paths.Home(homeFlag)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	err = nativehost.Run(nativehost.Options{
		Dir:         paths.WebClaude(home),
		Origin:      origin,
		ExtensionID: extension.ID(),
		// Off unless the collector's config says on; chat text also needs
		// the web-claude prompt setting, which the host reads itself.
		Prompts: func() bool { return webcapture.PromptCapture(home) },
		Version: version,
		In:      os.Stdin,
		Out:     stdout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// webCaptureArgs checks the web-capture subcommand and its arguments.
func webCaptureArgs(sub string, args []string) bool {
	switch sub {
	case "install", "status", "uninstall":
		return len(args) == 0
	case "sweeper", "infer-apps":
		return len(args) == 1 && (args[0] == "on" || args[0] == "off")
	case "prompts":
		return len(args) == 1 && (args[0] == "off" || args[0] == "live" || args[0] == "all")
	case "history-before":
		return len(args) == 1
	}
	return false
}
