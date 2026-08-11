package unattended

import "testing"

func TestParseVersionReadsRealToolBanners(t *testing.T) {
	cases := []struct {
		banner string
		want   string
	}{
		{"git version 2.43.0", "2.43.0"},
		{"gh version 2.97.0 (2026-07-31)", "2.97.0"},
		{"go version go1.26.5 linux/amd64", "1.26.5"},
		{"bd 1.1.0", "1.1.0"},
		{"dolt version 1.43.0", "1.43.0"},
		{"tmux 3.4", "3.4"},
		{"v20.11.1", "20.11.1"},
	}
	for _, c := range cases {
		got, ok := ParseVersion(c.banner)
		if !ok {
			t.Fatalf("ParseVersion(%q) failed to find a version", c.banner)
		}
		if FormatVersion(got) != c.want {
			t.Errorf("ParseVersion(%q) = %s, want %s", c.banner, FormatVersion(got), c.want)
		}
	}
}

func TestParseVersionRefusesTextWithNoVersion(t *testing.T) {
	for _, s := range []string{"", "command not found", "no version here"} {
		if _, ok := ParseVersion(s); ok {
			t.Errorf("ParseVersion(%q) claimed to find a version", s)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		have, want string
		ok         bool
	}{
		{"git version 2.43.0", "2.30", true},
		{"git version 2.43.0", "2.43.0", true},
		{"git version 2.43.0", "2.44", false},
		{"go version go1.26.5 linux/amd64", "1.24", true},
		{"go version go1.21.0 linux/amd64", "1.24", false},
		// A shorter observed version must not lose to an equal longer one.
		{"tmux 3.4", "3.4.0", true},
		{"tmux 3.4", "3.4.1", false},
	}
	for _, c := range cases {
		got, err := VersionAtLeast(c.have, c.want)
		if err != nil {
			t.Fatalf("VersionAtLeast(%q, %q): %v", c.have, c.want, err)
		}
		if got != c.ok {
			t.Errorf("VersionAtLeast(%q, %q) = %v, want %v", c.have, c.want, got, c.ok)
		}
	}
}

// An unreadable version is neither "too old" nor "new enough". Reporting either
// would be an invented answer, so it must be an error.
func TestVersionAtLeastErrorsOnUnparseableInput(t *testing.T) {
	if _, err := VersionAtLeast("command not found", "1.0"); err == nil {
		t.Error("an unparseable observed version returned a verdict instead of an error")
	}
	if _, err := VersionAtLeast("git version 2.43.0", "latest"); err == nil {
		t.Error("an unparseable requirement returned a verdict instead of an error")
	}
}
