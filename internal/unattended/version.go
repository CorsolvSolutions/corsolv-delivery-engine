package unattended

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// versionPattern finds the first dotted numeric run in a version banner.
//
// Tool banners are not a format. `git version 2.43.0`, `gh version 2.97.0
// (2026-07-31)`, `go version go1.26.5 linux/amd64` and `bd 1.1.0` agree on
// nothing except that the version is the first dotted number in the output.
// Matching that run is the whole rule, and text it cannot find a number in is
// reported unparseable rather than guessed at — a guessed version that reads as
// "new enough" is worse than no check.
var versionPattern = regexp.MustCompile(`\d+(?:\.\d+)*`)

// ParseVersion extracts the first dotted numeric version from arbitrary text.
func ParseVersion(s string) ([]int, bool) {
	m := versionPattern.FindString(s)
	if m == "" {
		return nil, false
	}
	parts := strings.Split(m, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// compareVersions orders two parsed versions, reading an absent component as
// zero so that 2.43 and 2.43.0 compare equal rather than unequal.
func compareVersions(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// VersionAtLeast reports whether the version found in have is at least want.
//
// An unparseable observed version is an error, never a false. A run that
// treated "could not read the version" as "the version is too old" would report
// a working machine as broken; a run that treated it as "new enough" would let
// an unknown toolchain into an unattended session. Neither is an answer, so
// neither is returned.
func VersionAtLeast(have, want string) (bool, error) {
	h, ok := ParseVersion(have)
	if !ok {
		return false, fmt.Errorf("no version number found in %q", strings.TrimSpace(have))
	}
	w, ok := ParseVersion(want)
	if !ok {
		return false, fmt.Errorf("required version %q is not a version number", want)
	}
	return compareVersions(h, w) >= 0, nil
}

// FormatVersion renders a parsed version back to dotted form, for reports.
func FormatVersion(v []int) string {
	parts := make([]string, 0, len(v))
	for _, n := range v {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ".")
}
