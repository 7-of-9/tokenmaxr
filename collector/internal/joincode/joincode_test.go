package joincode

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	k := bytes.Repeat([]byte{0xA5}, 32)
	for _, inv := range []string{"abc123", "Zx_9-qq-7", "i-" + strings.Repeat("A", 52)} {
		code := Format(inv, k)
		gotInv, gotK, err := Parse(code)
		if err != nil || gotInv != inv || !bytes.Equal(gotK, k) {
			t.Fatalf("Parse(%q) = %q, %x, %v", code, gotInv, gotK, err)
		}
		if strings.Contains(code, "=") {
			t.Fatalf("code has padding: %q", code)
		}
	}
}

func TestBootstrapCode(t *testing.T) {
	inv, k, err := Parse("  d0m1-invite-with-dash  ")
	if err != nil || inv != "invite-with-dash" || k != nil {
		t.Fatalf("got %q %x %v", inv, k, err)
	}
}

func TestLastSegmentThatIsNotAKey(t *testing.T) {
	// 52 chars but lowercase: not base32, so it stays part of the invite.
	seg := strings.Repeat("a", 52)
	inv, k, err := Parse("D0M1-x-" + seg)
	if err != nil || inv != "x-"+seg || k != nil {
		t.Fatalf("got %q %x %v", inv, k, err)
	}
}

func TestBadCodes(t *testing.T) {
	for _, c := range []string{"", "D0M1-", "XXXX-abc", "D0M1-has space"} {
		if _, _, err := Parse(c); err == nil {
			t.Errorf("Parse(%q) should fail", c)
		}
	}
}
