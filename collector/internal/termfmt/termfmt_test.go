package termfmt

import (
	"bytes"
	"strings"
	"testing"
)

func TestRowsWrapUnderTheirValue(t *testing.T) {
	var b bytes.Buffer
	p := Plain(&b, 40)
	p.Title("tokenmaxr 1.0", "windows/amd64")
	p.Section("Publishing")
	p.Row("GitHub", "octo/usage as \"laptop\", signed in as octo", "published 3 min ago")
	p.Row("evidence", "one two three four five six seven eight nine ten eleven")
	want := `tokenmaxr 1.0  windows/amd64

Publishing
  GitHub         octo/usage as "laptop",
                 signed in as octo
                 published 3 min ago
  evidence       one two three four five
                 six seven eight nine
                 ten eleven
`
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if Width(l) > 40 {
			t.Errorf("line wider than 40: %q", l)
		}
	}
}

func TestPlainHasNoColour(t *testing.T) {
	var b bytes.Buffer
	p := Plain(&b, 80)
	if p.Good("ok") != "ok" || p.Bad("x") != "x" {
		t.Fatal("colour without a terminal")
	}
	p.color = true
	if s := p.Good("ok"); s == "ok" || Width(s) != 2 {
		t.Fatalf("coloured %q width %d", s, Width(s))
	}
}

func TestWrapKeepsLongWords(t *testing.T) {
	got := Wrap("see C:\\Users\\someone\\AppData\\Local\\tokenmaxr\\collector.log now", 20)
	if len(got) != 3 || got[1] != "C:\\Users\\someone\\AppData\\Local\\tokenmaxr\\collector.log" {
		t.Fatalf("%q", got)
	}
}
