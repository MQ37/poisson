package tools

import (
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
)

// TestGlob_TrailingDoubleStarAbsorbsRemainder regression-guards a real
// correctness bug the DP rewrite (memoized matchStarStarTail, fixing the
// exponential-backtracking DoS) introduced and shipped once already: its
// first version required si == len(segs) at the "parts exhausted" base
// case, which broke any pattern ending in an unconstrained trailing "**"
// (including the extremely common "**/node_modules/**" exclude/include
// shape) — leftover path segments after the last real literal part must be
// silently absorbed by a trailing "**", not rejected.
func TestGlob_TrailingDoubleStarAbsorbsRemainder(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"a/**/b/**", "a/x/b/y/z", true},
		{"**/node_modules/**", "foo/bar/node_modules/pkg/x.js", true},
		{"**/node_modules/**", "node_modules/pkg/index.js", true},
		{"a/**/b/**", "a/x/c/y/z", false}, // "b" segment genuinely absent
	}
	for _, c := range cases {
		got, err := pathMatch(c.pattern, c.rel)
		if err != nil {
			t.Fatalf("pathMatch(%q, %q): %v", c.pattern, c.rel, err)
		}
		if got != c.want {
			t.Errorf("pathMatch(%q, %q) = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}

// TestFuzzGlobMatchAgainstReference cross-checks pathMatch against a
// brute-force reference matcher (regex-translated **) over random patterns
// and paths, to catch any remaining behavioral drift from the DP rewrite.
func TestFuzzGlobMatchAgainstReference(t *testing.T) {
	segs := []string{"a", "b", "c", "x.go", "nomatch"}
	r := rand.New(rand.NewSource(42))
	randPath := func() string {
		n := 1 + r.Intn(5)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = segs[r.Intn(len(segs))]
		}
		return strings.Join(parts, "/")
	}
	randPattern := func() string {
		n := 1 + r.Intn(4)
		parts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if r.Intn(3) == 0 {
				parts = append(parts, "**")
			} else {
				parts = append(parts, segs[r.Intn(len(segs))])
			}
		}
		return strings.Join(parts, "/")
	}
	for i := 0; i < 20000; i++ {
		pat := randPattern()
		p := randPath()
		got, err := pathMatch(pat, p)
		if err != nil {
			continue
		}
		want := refMatch(pat, p)
		if got != want {
			t.Fatalf("pathMatch(%q,%q)=%v, reference=%v", pat, p, got, want)
		}
	}
}

// refMatch is a naive reference: expand ** as "match zero or more full
// segments" via brute-force segment-count enumeration (slow, only for tests).
func refMatch(pattern, rel string) bool {
	pparts := strings.Split(pattern, "/")
	rparts := strings.Split(rel, "/")
	var rec func(pi, ri int) bool
	rec = func(pi, ri int) bool {
		if pi == len(pparts) {
			return ri == len(rparts)
		}
		if pparts[pi] == "**" {
			for k := ri; k <= len(rparts); k++ {
				if rec(pi+1, k) {
					return true
				}
			}
			return false
		}
		if ri == len(rparts) {
			return false
		}
		ok, _ := matchSeg(pparts[pi], rparts[ri])
		if !ok {
			return false
		}
		return rec(pi+1, ri+1)
	}
	return rec(0, 0)
}

func matchSeg(pat, s string) (bool, error) {
	return filepath.Match(pat, s)
}
