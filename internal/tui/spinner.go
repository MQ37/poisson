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
func needsSpinner(thinking bool, activeTools int, compacting bool) bool {
	return thinking || activeTools > 0 || compacting
}
