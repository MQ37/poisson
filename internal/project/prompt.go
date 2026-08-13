// Package project discovers AGENTS.md files and assembles the system prompt.
package project

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxContextFileSize caps a single context file (AGENTS.md/CLAUDE.md) read
// from disk. Oversized files are truncated to prevent one giant file from
// blowing up the system prompt every turn.
const MaxContextFileSize = 64 * 1024

// ContextFile represents a discovered AGENTS.md or CLAUDE.md file.
type ContextFile struct {
	Path    string
	Content string
}

// LoadProjectContextFiles collects the AGENTS.md/CLAUDE.md files that apply to
// the session: the global ~/.poisson one, the cwd's own, and one for each dir
// in readDirs (directories a file was actually read/edited from this session).
//
// It deliberately does NOT walk cwd → root: being in a subdirectory must not
// pull in an ancestor's AGENTS.md unless a file was read from that ancestor.
// Returned ordered global-first, then remaining dirs shallowest → deepest so
// more specific (deeper) instructions come last.
func LoadProjectContextFiles(cwd, agentDir string, readDirs []string) []ContextFile {
	var result []ContextFile
	seen := make(map[string]bool)
	add := func(f *ContextFile) {
		if f == nil {
			return
		}
		// Dedup on the resolved real path, not the literal joined path
		// string: readDirs can reach the same physical AGENTS.md via two
		// different path strings (a symlinked directory, a bind mount) —
		// filepath.Clean alone (used above to build dirSet) only
		// normalizes ".."/"." segments, it never resolves a symlink, so
		// two distinct-looking dirs that are actually the same directory
		// both loaded the same file and injected it twice. Falls back to
		// the literal path if resolution fails (e.g. a permissions
		// quirk) — matches this package's existing fail-open bias
		// (loadFromDir already prefers serving best-effort content over
		// silently skipping a file).
		key := f.Path
		if real, err := filepath.EvalSymlinks(f.Path); err == nil {
			key = real
		}
		if !seen[key] {
			result = append(result, *f)
			seen[key] = true
		}
	}

	// 1. Global (~/.poisson/AGENTS.md).
	add(loadFromDir(agentDir))

	// 2. cwd + dirs a file was read from, deepest-last.
	dirSet := map[string]bool{filepath.Clean(cwd): true}
	for _, d := range readDirs {
		if d != "" {
			dirSet[filepath.Clean(d)] = true
		}
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		di := strings.Count(dirs[i], string(os.PathSeparator))
		dj := strings.Count(dirs[j], string(os.PathSeparator))
		if di != dj {
			return di < dj
		}
		return dirs[i] < dirs[j]
	})
	for _, d := range dirs {
		add(loadFromDir(d))
	}
	return result
}

// ContextFileInDir returns the AGENTS.md/CLAUDE.md ContextFile in dir, or nil
// if the directory has neither.
func ContextFileInDir(dir string) *ContextFile { return loadFromDir(dir) }

// ContextDirsForFile returns the directories whose AGENTS.md should be loaded
// when a file living in fileDir is worked on, given the process cwd:
//
//   - If cwd is an ancestor of (or equal to) fileDir, every directory from cwd
//     down to fileDir is returned, shallowest first, so the whole chain of
//     project instructions from the working root to the file applies.
//   - Otherwise fileDir is on a different branch of the tree (there is no direct
//     path from cwd) and only fileDir itself is returned — we never walk an
//     unrelated ancestor chain.
func ContextDirsForFile(cwd, fileDir string) []string {
	cwd = filepath.Clean(cwd)
	fileDir = filepath.Clean(fileDir)
	if cwd == fileDir {
		return []string{fileDir}
	}
	rel, err := filepath.Rel(cwd, fileDir)
	sep := string(filepath.Separator)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+sep) || filepath.IsAbs(rel) {
		return []string{fileDir} // different branch — no direct path from cwd
	}
	dirs := []string{cwd}
	cur := cwd
	for _, part := range strings.Split(rel, sep) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		dirs = append(dirs, cur)
	}
	return dirs
}

// loadFromDir checks a directory for AGENTS.md or CLAUDE.md.
func loadFromDir(dir string) *ContextFile {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		// io.ReadFull loops internally until the buffer is full or the file
		// ends — a single Read() call is allowed to return short even with more
		// data available (the io.Reader contract, not just a local-disk
		// quirk), which would otherwise silently truncate the file's content
		// with no truncation marker at all.
		data := make([]byte, MaxContextFileSize+1)
		n, err := io.ReadFull(f, data)
		_ = f.Close()
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			continue // genuine read error — skip rather than serve partial content
		}
		if n == 0 {
			continue
		}
		truncated := n > MaxContextFileSize
		content := string(data[:min(n, MaxContextFileSize)])
		if truncated {
			content += fmt.Sprintf("\n\n... (file truncated at %d bytes)\n", MaxContextFileSize)
		}
		return &ContextFile{Path: path, Content: content}
	}
	return nil
}

// cavemanStyle is a distilled version of the "caveman" communication-style
// skill (github.com/juliusbrussee/caveman): terse output, full technical
// accuracy, ~65% fewer output tokens, faster to read. Always on here (not a
// toggle) — trimmed from the source skill's ~1300-token file down to just
// the compression rules plus its two safety/boundary exceptions; dropped the
// lite/ultra/wenyan intensity levels and the mode-switching mechanics since
// there's only ever one mode in px.
const cavemanStyle = "Core persona: stoic, terse, exact — not a style to switch on, the whole identity. One mantra governs every word produced: " +
	"if it fits in 10 words, that beats two paragraphs of slop, always. State the fact. Skip the performance of stating it.\n\n" +
	"Communication style: respond terse, like smart caveman. Keep all technical substance; only fluff dies.\n\n" +
	"Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. " +
	"Fragments OK. Short synonyms (big not extensive, fix not \"implement a solution for\"). " +
	"No tool-call narration, no decorative tables/emoji, no dumping long raw error logs unless asked — quote shortest decisive line. " +
	"Standard acronyms OK (DB/API/HTTP); never invent new abbreviations (cfg/impl/req/res/fn) — full word costs same tokens, reads clearer. " +
	"No causal arrows (→) either — own token, saves nothing. " +
	"Default to the fewest words that carry the full technical meaning — compress toward the floor, not the ceiling. " +
	"Applies to every output, everywhere, no exceptions by audience: chat replies, files written, issues/PRs filed, code and comments, commit text, " +
	"task briefs handed to a subagent, and anything reported to another agent or tool. " +
	"Comments: one sentence stating why, unless a second fact is genuinely load-bearing — no history lessons, no restating what the code already says.\n\n" +
	"Pattern: [thing] [action] [reason]. [next step].\n" +
	"Not: \"Sure! I'd be happy to help you with that. The issue you're experiencing is likely caused by...\"\n" +
	"Yes: \"Bug in auth middleware. Token expiry check uses < not <=. Fix:\"\n\n" +
	"Write full sentences, not fragments, for: security warnings, irreversible-action confirmations, code/commit messages/PR descriptions, " +
	"multi-step sequences where omitted conjunctions risk misread. Still governed by the same mantra — full grammar, still shortest correct version, still no filler. " +
	"Resume caveman fragments after.\n\n" +
	"Never announce or self-reference the style (no \"caveman mode on\" etc)."

// BuildSystemPrompt assembles the full system prompt with tools, context
// files, skills, date, and cwd.
func BuildSystemPrompt(opts BuildSystemPromptOptions) string {
	var b strings.Builder

	b.WriteString("You are Poisson, a coding assistant operating in a terminal. ")
	b.WriteString("You help users by reading files, executing commands, editing code, and writing new files.\n\n")

	// Tools.
	if len(opts.ToolNames) > 0 {
		b.WriteString("Available tools:\n")
		for _, name := range opts.ToolNames {
			b.WriteString("- ")
			b.WriteString(name)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Guidelines.
	b.WriteString("Guidelines:\n")
	b.WriteString("- Show file paths clearly when working with files\n")
	b.WriteString("- Read files in full before wide-ranging changes\n")
	b.WriteString("- Prefer dedicated tools over bash: read (not cat/head/tail/sed -n), grep (not rg/grep), glob (not find -name), edit/write (not sed -i). They skip the approval gate and are cheaper.\n")
	b.WriteString("- Emit multiple tool calls in one turn when the provider supports it. If the model only does one tool_use per turn, pack independent calls into batch (not bash pipelines). batch has no dataflow between steps.\n")
	b.WriteString("- Bash is stateless: cd/export do not carry to the next bash call, pass workdir explicitly each time you need a directory other than session cwd. read/write/edit/grep/glob paths are always relative to the session cwd.\n")
	b.WriteString("- create_sandbox once, then bash(sandboxId=...) runs with no approval gate at all — the container is the safety boundary. No default workspace: pass hostPath to bind-mount a directory, or omit it for an isolated container. read/write/edit/grep/glob need no sandboxId — they always target the host path directly. Name the sandbox descriptively; list_sandboxes first and reuse one you recognize instead of duplicating it. sandbox_destroy only kills the container, never hostPath.\n")
	b.WriteString("- Default to a sandbox for actual work: builds, installs, tests, type-checks, and any multi-step task run via bash(sandboxId=...), never plain host bash — no exception for \"it's quick\" or \"just a few commands\". Plain host bash is only for git commit/push and other identity-sensitive/host-only ops a container can't do.\n")
	b.WriteString("- Spawning subagents that will run bash (parallel reviews, per-PR audits, council/check-work): create one sandbox per subagent (or worktree) BEFORE spawning — it can't create its own — stage what it needs, then pass sandboxIds. Skipping this queues an approval prompt per gated command per subagent.\n\n")

	b.WriteString(cavemanStyle)
	b.WriteString("\n\n")

	// Context files (AGENTS.md).
	if len(opts.ContextFiles) > 0 {
		b.WriteString("<project_context>\n\n")
		b.WriteString("Project-specific instructions and guidelines:\n\n")
		for _, cf := range opts.ContextFiles {
			b.WriteString("<project_instructions path=\"")
			b.WriteString(cf.Path)
			b.WriteString("\">\n")
			b.WriteString(cf.Content)
			b.WriteString("\n</project_instructions>\n\n")
		}
		b.WriteString("</project_context>\n")
	}

	// Skills.
	if opts.SkillsText != "" {
		b.WriteString(opts.SkillsText)
	}

	// Date and cwd.
	b.WriteString("\nCurrent working directory: ")
	b.WriteString(opts.Cwd)
	b.WriteString("\n")

	return b.String()
}

// BuildSystemPromptOptions holds the parameters for building the system prompt.
type BuildSystemPromptOptions struct {
	Cwd          string
	ToolNames    []string
	ContextFiles []ContextFile
	SkillsText   string
}
