// Package tzinfo reports the machine's time zone (IANA and, on Windows, the
// Windows time-zone ID) and the ISO 3166-1 country it implies, so the
// dashboard can show a country flag per workstation.
//
// Mapping data is embedded: windowszones.tsv comes from the CLDR
// windowsZones table and zonecountry.tsv from IANA tzdata zone.tab plus the
// backward-compatibility aliases. Regenerate both with go generate.
package tzinfo

//go:generate go run gen/main.go

import (
	_ "embed"
	"os"
	"strings"
	"sync"
)

// Info describes the detected time zone of this machine.
type Info struct {
	// IANA is the tz database name, e.g. "Europe/London"; "" if unknown.
	IANA string `json:"iana,omitempty"`
	// WindowsID is the Windows time-zone key name, e.g. "GMT Standard Time"
	// (Windows only).
	WindowsID string `json:"windowsId,omitempty"`
	// Country is the ISO 3166-1 alpha-2 code in upper case, or "".
	Country string `json:"country,omitempty"`
	// Source is how IANA was determined: "tz-env", "iana",
	// "windows+region", "windows-golden" or "unknown".
	Source string `json:"source"`
}

// Source values.
const (
	SourceTZEnv         = "tz-env"
	SourceIANA          = "iana"
	SourceWindowsRegion = "windows+region"
	SourceWindowsGolden = "windows-golden"
	SourceUnknown       = "unknown"
)

var (
	//go:embed windowszones.tsv
	windowsZonesTSV string
	//go:embed zonecountry.tsv
	zoneCountryTSV string

	tablesOnce  sync.Once
	windowsMap  map[string]map[string]string // WindowsID -> territory -> IANA
	countryMap  map[string]string            // IANA (incl. aliases) -> CC
	detectOnce  sync.Once
	detectCache Info
)

func loadTables() {
	tablesOnce.Do(func() {
		windowsMap = make(map[string]map[string]string, 150)
		eachRow(windowsZonesTSV, func(f []string) {
			if len(f) != 3 {
				return
			}
			m := windowsMap[f[0]]
			if m == nil {
				m = map[string]string{}
				windowsMap[f[0]] = m
			}
			m[f[1]] = f[2]
		})
		countryMap = make(map[string]string, 600)
		eachRow(zoneCountryTSV, func(f []string) {
			if len(f) == 2 {
				countryMap[f[0]] = f[1]
			}
		})
	})
}

func eachRow(tsv string, fn func([]string)) {
	for _, line := range strings.Split(tsv, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		fn(strings.Split(line, "\t"))
	}
}

// Detect returns this machine's time zone and country. The result is
// computed once per process and cached.
func Detect() Info {
	detectOnce.Do(func() { detectCache = detect() })
	return detectCache
}

func detect() Info {
	if z := ianaFromTZEnv(os.Getenv("TZ")); z != "" {
		return Info{IANA: z, Country: CountryForIANA(z), Source: SourceTZEnv}
	}
	if info, ok := detectPlatform(); ok {
		return info
	}
	return Info{Source: SourceUnknown}
}

// ianaFromTZEnv returns the IANA name in a TZ value, or "" if the value is
// empty, a POSIX rule string (e.g. "EST5EDT,M3.2.0,M11.1.0") or an
// unrecognised path.
func ianaFromTZEnv(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), ":")
	if v == "" {
		return ""
	}
	if i := strings.LastIndex(v, "zoneinfo/"); i >= 0 {
		v = v[i+len("zoneinfo/"):]
	}
	return cleanIANA(v)
}

// cleanIANA validates a candidate tz database name and strips the
// "posix/" and "right/" zoneinfo subtree prefixes. It returns "" when the
// candidate does not look like an IANA name.
func cleanIANA(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "posix/")
	v = strings.TrimPrefix(v, "right/")
	if v == "" || strings.HasPrefix(v, "/") || strings.Contains(v, "..") {
		return ""
	}
	loadTables()
	if _, ok := countryMap[v]; ok {
		return v
	}
	switch v {
	case "UTC", "GMT", "UCT", "Zulu", "Universal", "Greenwich":
		return v
	}
	area, rest, ok := strings.Cut(v, "/")
	if !ok || area == "" || rest == "" {
		return ""
	}
	for _, r := range v {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			r == '/' || r == '_' || r == '-' || r == '+') {
			return ""
		}
	}
	return v
}

// CountryForIANA returns the ISO 3166-1 alpha-2 country (upper case) for an
// IANA zone name, including backward-compatible aliases such as
// "Asia/Calcutta", "Europe/Kiev" or "US/Pacific". It returns "" for unknown
// zones and for zones without a country (e.g. "Etc/UTC").
func CountryForIANA(zone string) string {
	loadTables()
	return countryMap[strings.TrimSpace(zone)]
}

// IANAForWindows maps a Windows time-zone ID (e.g. "W. Europe Standard
// Time") and an optional ISO 3166-1 alpha-2 region to an IANA zone using
// the CLDR windowsZones table: the region's territory row if CLDR lists one
// for that Windows ID, else the "001" golden zone. It returns "" for an
// unknown Windows ID.
func IANAForWindows(windowsID, region string) string {
	z, _ := ianaForWindows(windowsID, region)
	return z
}

// ianaForWindows is IANAForWindows plus whether the territory row was used.
func ianaForWindows(windowsID, region string) (zone string, byRegion bool) {
	loadTables()
	rows := windowsMap[strings.TrimSpace(windowsID)]
	if rows == nil {
		return "", false
	}
	if r := normRegion(region); r != "" {
		if z, ok := rows[r]; ok {
			return z, true
		}
	}
	return rows["001"], false
}

// normRegion returns an upper-case two-letter region code, or "".
func normRegion(region string) string {
	r := strings.ToUpper(strings.TrimSpace(region))
	if len(r) != 2 || r[0] < 'A' || r[0] > 'Z' || r[1] < 'A' || r[1] > 'Z' {
		return ""
	}
	return r
}

// fromWindows builds an Info from a Windows zone ID and the user's region.
func fromWindows(windowsID, region string) (Info, bool) {
	zone, byRegion := ianaForWindows(windowsID, region)
	if zone == "" {
		if windowsID == "" {
			return Info{}, false
		}
		return Info{WindowsID: windowsID, Source: SourceUnknown}, true
	}
	info := Info{IANA: zone, WindowsID: windowsID, Country: CountryForIANA(zone), Source: SourceWindowsGolden}
	if byRegion {
		info.Source = SourceWindowsRegion
	}
	return info, true
}
