package ebitenmcp

import "testing"

// A game whose logger checks isatty keeps seeing one, because the tee hands it
// the real descriptor. So it goes on emitting colour and every captured line
// arrives wrapped in escapes that mean nothing to the thing reading them.
func TestStripANSIKeepsTheTextAndDropsTheEscapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "nothing to do here", "nothing to do here"},
		{"colour", "\x1b[31mred\x1b[0m", "red"},
		{"bright", "\x1b[1;38;5;196mvery red\x1b[0m", "very red"},
		{"slog", "\x1b[92mINFO\x1b[0m tick=42 \x1b[2mmsg\x1b[0m=up", "INFO tick=42 msg=up"},
		{"cursor", "loading\x1b[2K\x1b[1Gdone", "loadingdone"},
		{"osc title", "\x1b]0;a title\x07after", "after"},
		{"osc st", "\x1b]8;;http://x\x1b\\link", "link"},
		{"lone escape", "before\x1b", "before"},

		// Spacing is left exactly as it was. Tidying it up is what their
		// implementation does, and it turns a stack trace into one long line.
		{"keeps tabs", "\x1b[31m\tgithub.com/x/y.f()\x1b[0m", "\tgithub.com/x/y.f()"},
		{"keeps runs of spaces", "a    b", "a    b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripANSI(tc.in); got != tc.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
