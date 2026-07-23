package guard

import "testing"

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
