---
name: create-skill
description: Author a new SKILL.md — as a builtin shipped in the px binary (internal/skills/builtin/<name>/SKILL.md, embedded via go:embed, no other code change needed) or as a user skill under ~/.poisson/skills/<name>/SKILL.md (picked up live, /reload, overrides a builtin of the same name). Use when asked to create, add, or write a new skill, teach px a skill, or scaffold a SKILL.md.
---

# Create Skill

A skill is one file: `SKILL.md`, YAML frontmatter plus a markdown body injected verbatim as the tool result when `skill` loads it. Write it as instructions for the agent, not documentation for a human.

## Frontmatter

```
---
name: my-skill          # optional — directory name wins if both set; keep them matching
description: ...        # the ONLY thing shown in the system prompt's skill list — decides if the agent ever picks this skill
argument-hint: [file]   # optional — shown after the description
---
```

`description` needs three things or the skill never gets used: what it does, when to use it (trigger phrases/situations), and enough specificity to stand apart from neighboring skills. Supports plain `key: value`, quoted strings, and YAML block scalars (`>` folds to spaces, `|` keeps newlines) for long descriptions — see `parseFrontmatter` in `internal/skills/skills.go`.

## Body

Actionable steps, not narrative. Use an existing skill as a template — `internal/skills/builtin/check-work/SKILL.md` or `feature-impact/SKILL.md` — numbered procedure, a table for anti-patterns/checklist, a "See also" cross-linking related skills.

## Option A — builtin (ships in the px binary)

1. Create `internal/skills/builtin/<name>/SKILL.md` in the poisson repo.
2. Nothing else needed in code — `skills.go` embeds the whole `builtin/` tree (`//go:embed builtin`) and walks every subdirectory at `Discover()` time, so a new folder is picked up on the next build automatically.
3. Add `<name>` to the `want` list in `TestBuiltinSkillsPresent` (`internal/skills/skills_test.go`) so a refactor can't silently drop it.
4. Add a row to the "Built-in skills" table in `README.md`, bump the count mentioned there.
5. `./build.sh` (re-embeds), then `go test ./...`.

Ships to every user on the next release. Only for skills genuinely useful across projects — not one repo's local workflow.

## Option B — user skill (this machine only)

1. `mkdir -p ~/.poisson/skills/<name>`
2. Write `~/.poisson/skills/<name>/SKILL.md` with the same frontmatter and body shape.
3. Run `/reload` in a running session (or start a new one) — no rebuild needed; `Discover()` re-scans `~/.poisson/skills/` from disk every call.
4. A user skill named identically to a builtin **replaces** it entirely (`skills.go`: user entries overwrite builtin ones in the same name-keyed map) — use this to customize a builtin without touching the binary.

Use for anything project-specific, personal-workflow, or experimental before it's proven enough to upstream as a builtin.

## Checklist

- [ ] `description` states what + when + how it differs from neighbors.
- [ ] Body is instructions, not documentation.
- [ ] Builtin: added to `TestBuiltinSkillsPresent`, README table, rebuilt.
- [ ] User: correct path `~/.poisson/skills/<name>/SKILL.md`, `/reload` run.
- [ ] Cross-linked from/to related skills in "See also".
