package tui

// spinnerFrames is the braille animation sequence used while streaming.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerChar returns the frame for the given tick counter.
func spinnerChar(frame int) string {
	if len(spinnerFrames) == 0 {
		return " "
	}
	if frame < 0 {
		frame = 0
	}
	return spinnerFrames[frame%len(spinnerFrames)]
}

// needsSpinner reports whether the render loop should keep ticking for
// animation — also true while compacting, so the header spinner animates
// and the user has some sign of life during what can be a slow LLM call.
// runningSubagent covers the case async subagents introduced: the main
// turn can go fully idle (thinking=false, no active tools) while a
// background subagent job is still running — its pinned widget still needs
// its spinner/live timer to animate even though nothing else is happening.
func needsSpinner(thinking bool, activeTools int, compacting, runningSubagent bool) bool {
	return thinking || activeTools > 0 || compacting || runningSubagent
}
