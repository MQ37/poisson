package guard

import (
	"strings"
	"testing"
)

// Per-command danger detectors are the last line of defense before a
// command is deemed auto-safe (no LLM call, no human prompt in Fast mode —
// see agent.WrapRiskGatedApproval), so every branch gets an explicit case
// here instead of being exercised only incidentally through Classify.

func TestFindHasDangerousFlag(t *testing.T) {
	dangerous := []string{
		"find . -delete",
		"find . -exec rm {} ;",
		"find . -execdir rm {} +",
		"find . -ok rm {} ;",
		"find . -okdir rm {} ;",
		"find . -fprintf out.txt %p",
		"find . -fls out.txt",
		"find . -fprint out.txt",
		"find . -fprint0 out.txt",
	}
	for _, cmd := range dangerous {
		if !findHasDangerousFlag(tokenize(cmd)) {
			t.Errorf("findHasDangerousFlag(%q) = false, want true", cmd)
		}
	}
	safe := []string{
		"find . -name *.go",
		"find . -iname settings.json -not -path */node_modules/*",
		"find . -maxdepth 2 -type f",
		"find . -print0",
	}
	for _, cmd := range safe {
		if findHasDangerousFlag(tokenize(cmd)) {
			t.Errorf("findHasDangerousFlag(%q) = true, want false", cmd)
		}
	}
}

func TestGhApiIsMutating(t *testing.T) {
	mutating := []string{
		"gh api repos/x/y/issues --method POST -f title=x",
		"gh api repos/x/y/issues -X POST",
		"gh api repos/x/y -X DELETE",
		"gh api repos/x/y -X PATCH",
		"gh api repos/x/y --method PUT",
		"gh api repos/x/y -X post", // case-insensitive
		// Implicit POST via -f/-F/--raw-field/--field — no --method/-X at
		// all. This is the common real-world way to mutate via gh api
		// (issue/comment/collaborator/gist creation, repo settings), per
		// gh's own docs: "default HTTP request method is GET ... POST if
		// any parameters were added".
		"gh api repos/x/y/issues -f title=pwn",
		"gh api repos/x/y/collaborators/attacker -f permission=admin",
		"gh api gists -F public=true",
		"gh api repos/x/y --raw-field archived=true",
		"gh api repos/x/y --field archived=true",
		// GraphQL always POSTs; a mutation there is indistinguishable from
		// a query by argument shape alone, so the endpoint itself is
		// enough to flag it.
		"gh api graphql -f query=mutation{deleteRepository}",
	}
	for _, cmd := range mutating {
		if !ghApiIsMutating(tokenize(cmd)) {
			t.Errorf("ghApiIsMutating(%q) = false, want true", cmd)
		}
	}
	safe := []string{
		"gh api repos/x/y/issues",
		"gh api repos/x/y --method GET",
		"gh api repos/x/y -X GET",
		// Explicit GET always wins, even with field params (gh sends them
		// as a query string instead of a POST body).
		"gh api repos/x/y --method GET -f q=1",
	}
	for _, cmd := range safe {
		if ghApiIsMutating(tokenize(cmd)) {
			t.Errorf("ghApiIsMutating(%q) = true, want false", cmd)
		}
	}
}

func TestGitSubIsMutating(t *testing.T) {
	mutating := []string{
		"git push origin main",
		"git commit -m wip",
		"git rebase -i HEAD~3",
		"git reset --hard",
		"git clean -fd",
		"git merge feature",
		"git cherry-pick abc123",
		"git revert abc123",
		"git am patch.diff",
		"git apply patch.diff",
		"git init",
		"git clone https://example.com/repo.git",
		"git fetch origin",
		"git pull",
		"git mv a b",
		"git update-ref refs/heads/x abc123",
		"git reflog expire --all",
		"git config user.name x",
		"git stash drop",
		"git stash pop",
		"git stash clear",
		"git branch -d feature",
		"git branch -D feature",
		"git branch --delete feature",
		"git branch -m old new",
		"git branch --move old new",
		"git tag -d v1.0",
		"git tag --delete v1.0",
		"git remote rm origin",
		"git remote add origin url",
		"git remote rename old new",
		"git remote set-url origin url",
		// Force-move an existing ref — doesn't delete, but silently
		// repoints a release tag or a branch (possibly main) to an
		// arbitrary commit with no fast-forward safety check.
		"git branch -f main HEAD~50",
		"git branch --force main HEAD~50",
		"git tag -f v1.0.0 HEAD~50",
		"git tag --force v1.0.0 HEAD~50",
	}
	for _, cmd := range mutating {
		if !gitSubIsMutating(tokenize(cmd)) {
			t.Errorf("gitSubIsMutating(%q) = false, want true", cmd)
		}
	}
	safe := []string{
		"git status",
		"git diff",
		"git log --oneline",
		"git show HEAD",
		"git branch",
		"git branch -a",
		"git remote -v",
		"git remote show origin",
		"git stash list",
		"git stash show",
		"git tag",
		"git tag -l",
	}
	for _, cmd := range safe {
		if gitSubIsMutating(tokenize(cmd)) {
			t.Errorf("gitSubIsMutating(%q) = true, want false", cmd)
		}
	}
}

func TestGitHasOutputFlag(t *testing.T) {
	if !gitHasOutputFlag(tokenize("git diff -o out.patch")) {
		t.Error("expected -o to be flagged")
	}
	if !gitHasOutputFlag(tokenize("git log --output out.txt")) {
		t.Error("expected --output to be flagged")
	}
	if !gitHasOutputFlag(tokenize("git show --output-file out.txt")) {
		t.Error("expected --output-file to be flagged")
	}
	if gitHasOutputFlag(tokenize("git log --oneline")) {
		t.Error("expected --oneline to not be flagged")
	}
}

func TestRgHasDangerousFlag(t *testing.T) {
	dangerous := []string{
		"rg -z pattern file.gz",
		"rg --null-data pattern",
		"rg --null pattern",
		"rg --pre=/tmp/x.sh pattern",
		"rg --pre-glob=*.sh pattern",
	}
	for _, cmd := range dangerous {
		if !rgHasDangerousFlag(tokenize(cmd)) {
			t.Errorf("rgHasDangerousFlag(%q) = false, want true", cmd)
		}
	}
	safe := []string{
		"rg -n pattern file.go",
		"rg -i pattern",
		"rg -A3 -B3 pattern",
	}
	for _, cmd := range safe {
		if rgHasDangerousFlag(tokenize(cmd)) {
			t.Errorf("rgHasDangerousFlag(%q) = true, want false", cmd)
		}
	}
}

func TestSedHasDangerousFlag(t *testing.T) {
	dangerous := []string{
		"sed -i s/a/b/ file.txt",
		"sed -i.bak s/a/b/ file.txt",
		"sed --in-place s/a/b/ file.txt",
		"sed --i s/a/b/ file.txt",  // GNU abbreviation
		"sed --in s/a/b/ file.txt", // GNU abbreviation
	}
	for _, cmd := range dangerous {
		if !sedHasDangerousFlag(tokenize(cmd)) {
			t.Errorf("sedHasDangerousFlag(%q) = false, want true", cmd)
		}
	}
	safe := []string{
		"sed -n 1,5p file.txt",
		"sed s/a/b/ file.txt",
		"sed -e s/a/b/ file.txt",
	}
	for _, cmd := range safe {
		if sedHasDangerousFlag(tokenize(cmd)) {
			t.Errorf("sedHasDangerousFlag(%q) = true, want false", cmd)
		}
	}
}

func TestSedScriptIsDangerous(t *testing.T) {
	dangerous := []string{
		`sed '1w out.txt' file.txt`,
		`sed -e '1w out.txt' file.txt`,
		`sed '1e whoami' file.txt`,
		`sed 's/a/b/w out.txt' file.txt`,
	}
	for _, cmd := range dangerous {
		if !sedScriptIsDangerous(tokenize(cmd)) {
			t.Errorf("sedScriptIsDangerous(%q) = false, want true", cmd)
		}
	}
	// Regression: an ordinary English word starting with 'w' or 'e' right
	// after a non-alpha byte (here '/') must not be mistaken for a sed w/e
	// command letter — both sides of the candidate letter must be isolated,
	// not just the left side.
	safe := []string{
		`sed 's/a/b/' file.txt`,
		`sed -n '1,5p' file.txt`,
		`sed '/word/d' file.txt`,
		`sed '/error/p' file.txt`,
		`sed '/warning/d' file.txt`,
		`sed '/each/s/x/y/' file.txt`,
	}
	for _, cmd := range safe {
		if sedScriptIsDangerous(tokenize(cmd)) {
			t.Errorf("sedScriptIsDangerous(%q) = true, want false", cmd)
		}
	}
}

func TestTreeHasDangerousFlag(t *testing.T) {
	if !treeHasDangerousFlag(tokenize("tree -o out.txt")) {
		t.Error("expected -o to be flagged")
	}
	if !treeHasDangerousFlag(tokenize("tree --output-file out.txt")) {
		t.Error("expected --output-file to be flagged")
	}
	if treeHasDangerousFlag(tokenize("tree -L 2")) {
		t.Error("expected -L to not be flagged")
	}
}

func TestYqHasDangerousFlag(t *testing.T) {
	dangerous := []string{
		"yq -i .a=1 file.yaml",
		"yq --inplace .a=1 file.yaml",
		"yq --in-place .a=1 file.yaml",
	}
	for _, cmd := range dangerous {
		if !yqHasDangerousFlag(tokenize(cmd)) {
			t.Errorf("yqHasDangerousFlag(%q) = false, want true", cmd)
		}
	}
	if yqHasDangerousFlag(tokenize("yq .a file.yaml")) {
		t.Error("expected plain read to not be flagged")
	}
}

func TestTailHasFollowFlag(t *testing.T) {
	if !tailHasFollowFlag(tokenize("tail -f log.txt")) {
		t.Error("expected -f to be flagged")
	}
	if !tailHasFollowFlag(tokenize("tail --follow log.txt")) {
		t.Error("expected --follow to be flagged")
	}
	if tailHasFollowFlag(tokenize("tail -n 20 log.txt")) {
		t.Error("expected -n to not be flagged")
	}
}

func TestMatchesFlag(t *testing.T) {
	cases := []struct {
		token, short, long string
		want               bool
	}{
		{"-i", "-i", "", true},
		{"-i.bak", "-i", "", true},
		{"--in-place", "", "--in-place", true},
		{"--in-place=file", "", "--in-place", true},
		{"--in", "", "--in-place", true},   // unambiguous abbreviation
		{"--i", "", "--in-place", true},    // unambiguous abbreviation
		{"--in-p", "", "--in-place", true}, // unambiguous abbreviation
		{"--inp", "", "--in-place", false}, // NOT a prefix: "--in-place" has a literal '-' where this has 'p'
		{"-x", "-i", "", false},
		{"--other", "", "--in-place", false},
		{"in-place", "", "--in-place", false}, // missing leading --
	}
	for _, c := range cases {
		if got := matchesFlag(c.token, c.short, c.long); got != c.want {
			t.Errorf("matchesFlag(%q, %q, %q) = %v, want %v", c.token, c.short, c.long, got, c.want)
		}
	}
}

// TestStripEmbeddedQuotes_SeesThroughQuoteSplicing locks down the exact
// obfuscation class normalizeToken must see through: a command name built
// from adjacent quoted/unquoted fragments (real, valid bash — quotes just
// group characters, they don't add a separator) that a naive
// leading/trailing-only trim would miss entirely.
func TestStripEmbeddedQuotes_SeesThroughQuoteSplicing(t *testing.T) {
	cases := []struct{ in, want string }{
		{"r''m", "rm"},
		{"'r'm", "rm"},
		{`r"m"`, "rm"},
		{`rm""`, "rm"},
		{"r'm'", "rm"},
		{"ls", "ls"},   // unaffected — no quotes at all
		{"'ls'", "ls"}, // whole token quoted — same as before
		{"cat", "cat"},
	}
	for _, c := range cases {
		if got := stripEmbeddedQuotes(c.in); got != c.want {
			t.Errorf("stripEmbeddedQuotes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestClassify_QuoteSplicedDestructiveCommand is the end-to-end version:
// guard.Classify must deterministically reject a quote-spliced "rm", not
// just fail to recognize it as safe by accident.
func TestClassify_QuoteSplicedDestructiveCommand(t *testing.T) {
	cases := []string{
		`r''m -rf /tmp/x`,
		`'r'm -rf /tmp/x`,
		`r"m" -rf /tmp/x`,
		`R''M -rf /tmp/x`,
	}
	for _, cmd := range cases {
		safe, reason := Classify(cmd)
		if safe {
			t.Errorf("Classify(%q) = safe, want unsafe (quote-spliced rm)", cmd)
		}
		if !strings.Contains(reason, "destructive") {
			t.Errorf("Classify(%q) reason = %q, want it recognized as a destructive command, not just \"not in safe list\"", cmd, reason)
		}
	}
}

// TestSafeListCommandName_RejectsLookalikes locks down the fix for a real
// command-name-spoofing gap: normalizeToken's path-Base stripping is
// correct for the "flag as dangerous" direction (over-inclusive there is
// harmless) but was, before this fix, also used to decide SAFE-list trust —
// letting a relative or arbitrary-absolute path whose final component
// merely spells a safe-listed name (e.g. "./evil/Cat", planted by an
// attacker or a manipulated agent in any writable directory) auto-run with
// zero LLM/human review, and incidentally letting malformed fragments like
// "(0000/Cd" (found by fuzzing) slip through as "cd" too.
func TestSafeListCommandName_RejectsLookalikes(t *testing.T) {
	notEligible := []string{
		"./evil/Cat",       // relative path lookalike
		"evil/Cat",         // relative, no leading "./"
		"/tmp/attacker/ls", // absolute but writable-dir lookalike
		"0000/Cd",          // malformed fragment, coincidental Base match
		"garbage/Cat",
	}
	for _, tok := range notEligible {
		if got := safeListCommandName(tok); got != "" {
			t.Errorf("safeListCommandName(%q) = %q, want \"\" (not eligible for SAFE-list trust)", tok, got)
		}
	}
	eligible := []struct{ tok, want string }{
		{"cat", "cat"},          // bare — real $PATH lookup
		{"CAT", "cat"},          // case-insensitive
		{"/usr/bin/git", "git"}, // standard system directory
		{"/bin/cat", "cat"},
		{"/usr/local/bin/rg", "rg"},
	}
	for _, c := range eligible {
		if got := safeListCommandName(c.tok); got != c.want {
			t.Errorf("safeListCommandName(%q) = %q, want %q", c.tok, got, c.want)
		}
	}
}

// TestClassify_RejectsCommandNameLookalike is the end-to-end version: a
// path-qualified lookalike must fail the SAFE list (fall through to
// LLM/human review), not auto-run.
func TestClassify_RejectsCommandNameLookalike(t *testing.T) {
	cmds := []string{
		"./evil/Cat notes.txt",
		"evil/Cat notes.txt",
		"/tmp/attacker/ls -la",
		"(0000/Cd",
		"{0000/Cd",
	}
	for _, cmd := range cmds {
		if safe, reason := Classify(cmd); safe {
			t.Errorf("Classify(%q) = safe (reason=%q), want unsafe", cmd, reason)
		}
	}
	// Sanity: the legitimate forms these are impersonating still work.
	legit := []string{"cat notes.txt", "/usr/bin/git status", "ls -la"}
	for _, cmd := range legit {
		if safe, reason := Classify(cmd); !safe {
			t.Errorf("Classify(%q) = unsafe (%s), want safe", cmd, reason)
		}
	}
}

func TestNormalizeTokenPathPrefix(t *testing.T) {
	cases := []struct{ in, out string }{
		{"/usr/bin/rg", "rg"},
		{"./sed", "sed"},
		{"SED", "sed"},
		{" git ", "git"},
	}
	for _, c := range cases {
		if got := normalizeToken(c.in); got != c.out {
			t.Errorf("normalizeToken(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}
