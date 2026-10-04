// Command buildflags prints the -X linker flags that stamp release.json's
// release identity into a build (internal/buildinfo), so every build path uses
// one mapping:
//
//	go build -ldflags "-s -w $(go run ./cmd/buildflags release.json)" ./cmd/d0m1-collector
package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

func main() {
	path := "release.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildflags:", err)
		os.Exit(1)
	}
	var rel map[string]string
	if err := json.Unmarshal(b, &rel); err != nil {
		fmt.Fprintln(os.Stderr, "buildflags:", err)
		os.Exit(1)
	}
	var flags []string
	for _, field := range slices.Sorted(maps.Keys(buildinfo.LDFlagNames)) {
		v, ok := rel[field]
		// Only the default server may be empty (none: GitHub or local only).
		if !ok || (v == "" && field != "defaultEndpoint") || strings.ContainsAny(v, " \t\n'\"") {
			fmt.Fprintf(os.Stderr, "buildflags: release.json field %q is missing or contains whitespace/quotes\n", field)
			os.Exit(1)
		}
		flags = append(flags, "-X github.com/7-of-9/tokenmaxr/collector/internal/buildinfo."+buildinfo.LDFlagNames[field]+"="+v)
	}
	fmt.Print(strings.Join(flags, " "))
}
