package main

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// calibration is the chars-per-token ratio of the Claude Web estimator
// (collector/extension/estimate.js, SPEC "Claude chat estimates"). It reads
// only local Claude Code transcripts and reports aggregate numbers: the
// sample size and the fitted ratio, never text, paths or ids.
type calibration struct {
	Messages int     `json:"messages"`
	Files    int     `json:"files"`
	Rho      float64 `json:"rho"`
	Median   float64 `json:"medianRho"`
	ASCII    int64   `json:"asciiChars"`
	Tokens   int64   `json:"outputTokens"`
}

// charClasses counts what estimate.js counts: ASCII, CJK (1 token each) and
// other non-ASCII characters (half a token each).
func charClasses(s string) (ascii, cjk, other int64) {
	for _, r := range s {
		switch {
		case r < 128:
			ascii++
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			cjk++
		default:
			other++
		}
	}
	return
}

type calMsg struct {
	textOnly         bool
	ascii, cjk, oth  int64
	out              int64
	seenBlockStrings map[string]bool
}

// calibrate fits rho over assistant messages whose every content block is
// text (no thinking, no tool use), with exact output_tokens.
func calibrate(home string) (calibration, error) {
	root := filepath.Join(home, ".claude", "projects")
	msgs := map[string]*calMsg{}
	files := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return fs.SkipAll
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		files++
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			b := sc.Bytes()
			if !strings.Contains(string(b[:min(len(b), 200)]), `"assistant"`) && !strings.Contains(string(b), `"type":"assistant"`) {
				continue
			}
			var l struct {
				Type    string `json:"type"`
				Message struct {
					ID      string `json:"id"`
					Model   string `json:"model"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					Usage *struct {
						OutputTokens int64 `json:"output_tokens"`
						Details      *struct {
							Thinking int64 `json:"thinking_tokens"`
						} `json:"output_tokens_details"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(b, &l) != nil || l.Type != "assistant" || l.Message.ID == "" || l.Message.Usage == nil || l.Message.Model == "<synthetic>" {
				continue
			}
			m := msgs[l.Message.ID]
			if m == nil {
				m = &calMsg{textOnly: true, seenBlockStrings: map[string]bool{}}
				msgs[l.Message.ID] = m
			}
			if d := l.Message.Usage.Details; d != nil && d.Thinking > 0 {
				m.textOnly = false
			}
			m.out = max(m.out, l.Message.Usage.OutputTokens)
			for _, c := range l.Message.Content {
				if c.Type != "text" {
					m.textOnly = false
					continue
				}
				// Streamed lines repeat a block; count each block text once.
				if m.seenBlockStrings[c.Text] {
					continue
				}
				m.seenBlockStrings[c.Text] = true
				a, k, o := charClasses(c.Text)
				m.ascii, m.cjk, m.oth = m.ascii+a, m.cjk+k, m.oth+o
			}
		}
		return nil
	})
	if err != nil {
		return calibration{}, err
	}
	var c calibration
	c.Files = files
	var ratios []float64
	for _, m := range msgs {
		rest := float64(m.out) - float64(m.cjk) - 0.5*float64(m.oth)
		if !m.textOnly || m.ascii < 200 || rest <= 0 {
			continue
		}
		c.Messages++
		c.ASCII += m.ascii
		c.Tokens += int64(rest)
		ratios = append(ratios, float64(m.ascii)/rest)
	}
	if c.Tokens > 0 {
		c.Rho = float64(c.ASCII) / float64(c.Tokens)
	}
	if len(ratios) > 0 {
		slices.Sort(ratios)
		c.Median = ratios[len(ratios)/2]
	}
	return c, nil
}
