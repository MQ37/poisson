package tui

// InstallStartupIntro paints the welcome banner into scrollback (visible inside the TUI).
func (t *TUI) InstallStartupIntro(version, provider, model string) {
	t.startupIntro = startupIntroMeta{
		version: version, provider: provider, model: model, installed: true,
	}
	t.prependStartupIntroLocked()
	t.introScrollTop = true
}

// prependStartupIntroLocked inserts the welcome banner at the top of scrollback.
// Caller must hold t.mu when invoked from locked paths.
func (t *TUI) prependStartupIntroLocked() {
	if !t.startupIntro.installed {
		return
	}
	lines := poissonIntroANSILines(t.startupIntro.version, t.startupIntro.provider, t.startupIntro.model)
	t.scroll.prependIntroLines(lines)
}

// clearScrollbackKeepIntroLocked replaces scrollback content but keeps the welcome banner.
func (t *TUI) clearScrollbackKeepIntroLocked() {
	t.scroll = newScrollback(8192)
	t.prependStartupIntroLocked()
}

func poissonIntroANSILines(version, provider, model string) []string {
	var out []string
	out = append(out, "")
	out = append(out, bold+fgCyan+"Σ"+reset)
	out = append(out, "")
	out = append(out, dim+"Embrace the entropy, probabilities favor the bold."+reset)
	out = append(out, "")
	out = append(out, dim+"Poisson "+version+" · "+provider+"/"+model+reset)
	out = append(out, "")
	return out
}
