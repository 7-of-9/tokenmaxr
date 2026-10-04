package tray

import "testing"

func TestBuildLabel(t *testing.T) {
	cases := map[[2]string]string{
		{"0.2.6", "2026-10-04T08:15:42Z"}:      "v0.2.6 · built 2026-10-04 08:15 UTC",
		{"0.2.6", "2026-10-04T15:15:42+07:00"}: "v0.2.6 · built 2026-10-04 08:15 UTC",
		{"0.2.6", ""}:                          "v0.2.6",
		{"0.2.6", "not-a-time"}:                "v0.2.6",
		{"dev", "2026-10-04T08:15:42Z"}:        "dev build",
		{"", ""}:                               "dev build",
	}
	for in, want := range cases {
		if got := BuildLabel(in[0], in[1]); got != want {
			t.Errorf("BuildLabel(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestPopupEndsWithInertBuildFooter(t *testing.T) {
	lines := Popup(Evaluate(Input{Version: "0.2.6", BuildTime: "2026-10-04T08:15:00Z"}))
	last := lines[len(lines)-1]
	if last.Text != "v0.2.6 · built 2026-10-04 08:15 UTC" || last.Kind != LineDim || last.Action != ActNone {
		t.Fatalf("footer = %+v", last)
	}
	if last.Hoverable() {
		t.Fatal("the build footer must not be hoverable or clickable")
	}
	if prev := lines[len(lines)-2]; prev.Action != ActQuit {
		t.Fatalf("Quit must stay the last action, got %+v", prev)
	}
}
