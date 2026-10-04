package tzinfo

import "testing"

func TestWindowsMapping(t *testing.T) {
	cases := []struct {
		id, region, zone, country, source string
	}{
		{"GMT Standard Time", "GB", "Europe/London", "GB", SourceWindowsRegion},
		{"Singapore Standard Time", "", "Asia/Singapore", "SG", SourceWindowsGolden},
		{"Singapore Standard Time", "SG", "Asia/Singapore", "SG", SourceWindowsRegion},
		{"W. Europe Standard Time", "NL", "Europe/Amsterdam", "NL", SourceWindowsRegion},
		{"W. Europe Standard Time", "nl", "Europe/Amsterdam", "NL", SourceWindowsRegion},
		{"W. Europe Standard Time", "", "Europe/Berlin", "DE", SourceWindowsGolden},
		{"W. Europe Standard Time", "US", "Europe/Berlin", "DE", SourceWindowsGolden},
		{"Pacific Standard Time", "", "America/Los_Angeles", "US", SourceWindowsGolden},
		{"AUS Eastern Standard Time", "AU", "Australia/Sydney", "AU", SourceWindowsRegion},
		{"India Standard Time", "IN", "Asia/Kolkata", "IN", SourceWindowsRegion},
	}
	for _, c := range cases {
		if got := IANAForWindows(c.id, c.region); got != c.zone {
			t.Errorf("IANAForWindows(%q,%q) = %q, want %q", c.id, c.region, got, c.zone)
		}
		info, ok := fromWindows(c.id, c.region)
		if !ok || info.IANA != c.zone || info.Country != c.country || info.Source != c.source || info.WindowsID != c.id {
			t.Errorf("fromWindows(%q,%q) = %+v, want %s/%s/%s", c.id, c.region, info, c.zone, c.country, c.source)
		}
	}
	if got := IANAForWindows("Nowhere Standard Time", "GB"); got != "" {
		t.Errorf("unknown windows id -> %q, want empty", got)
	}
	if info, ok := fromWindows("Nowhere Standard Time", "GB"); !ok || info.Source != SourceUnknown || info.IANA != "" || info.Country != "" {
		t.Errorf("fromWindows(unknown) = %+v, %v", info, ok)
	}
}

func TestCountryForIANA(t *testing.T) {
	cases := map[string]string{
		"Europe/London":       "GB",
		"Europe/Amsterdam":    "NL",
		"Asia/Calcutta":       "IN",
		"Asia/Kolkata":        "IN",
		"Europe/Kiev":         "UA",
		"US/Pacific":          "US",
		"America/Los_Angeles": "US",
		"Etc/UTC":             "",
		"Mars/Olympus_Mons":   "",
		"":                    "",
	}
	for zone, want := range cases {
		if got := CountryForIANA(zone); got != want {
			t.Errorf("CountryForIANA(%q) = %q, want %q", zone, got, want)
		}
	}
}

func TestTZEnv(t *testing.T) {
	cases := map[string]string{
		"Europe/London":                     "Europe/London",
		":Asia/Singapore":                   "Asia/Singapore",
		"/usr/share/zoneinfo/Europe/Berlin": "Europe/Berlin",
		"UTC":                               "UTC",
		"EST5EDT,M3.2.0,M11.1.0":            "",
		"":                                  "",
		"../../etc/passwd":                  "",
	}
	for in, want := range cases {
		if got := ianaFromTZEnv(in); got != want {
			t.Errorf("ianaFromTZEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectCached(t *testing.T) {
	a, b := Detect(), Detect()
	if a != b || a.Source == "" {
		t.Fatalf("Detect not stable: %+v vs %+v", a, b)
	}
}
