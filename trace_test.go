package ebitenmcp

import (
	"errors"
	"strings"
	"testing"
)

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

// TestTracesSayWhyItHasNothingRatherThanJustNothing.
//
// An empty list of lines means two opposite things: the game printed nothing,
// or nothing was ever captured. The first wants you to look elsewhere; the
// second wants you to stop expecting this tool to answer on this platform.
// Descriptor duplication is how the capture works, and Windows has none — so
// there, silently, game_traces used to answer as though the game had been
// quiet.
func TestTracesSayWhyItHasNothingRatherThanJustNothing(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// A ring that never managed to tap anything, which is what a platform
	// without dup leaves behind.
	s.traces = newTraceRing()
	s.traces.setIncomplete(errNoFDCapture)

	_, out, err := s.tracesTool(ctx, nil, tracesInput{})
	if err != nil {
		t.Fatalf("game_traces: %v", err)
	}

	if len(out.Lines) != 0 {
		t.Fatalf("a ring that captured nothing has %d lines", len(out.Lines))
	}
	if out.Unavailable == "" {
		t.Fatal("it answered with no lines and no reason, which reads as a quiet game")
	}
	if !strings.Contains(out.Unavailable, "unix") {
		t.Errorf("the reason does not say what is missing: %q", out.Unavailable)
	}
}

// TestTracesSayHalfACaptureIsHalfACapture is the case that nearly got away.
//
// The two descriptors are tapped separately, so stdout can be captured while
// stderr is not — and stderr is where the panics go. Lines in hand then read as
// the whole story, which is the failure this was supposed to remove rather than
// move. Saying it only when there are no lines at all would have left it.
func TestTracesSayHalfACaptureIsHalfACapture(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	s.traces = newTraceRing()
	s.traces.add("stdout", "something the game printed", 1)
	s.traces.setIncomplete(errors.New("stderr is not being captured: no pipes left"))

	_, out, err := s.tracesTool(ctx, nil, tracesInput{})
	if err != nil {
		t.Fatalf("game_traces: %v", err)
	}

	if len(out.Lines) == 0 {
		t.Fatal("the line went missing")
	}
	if out.Unavailable == "" {
		t.Error("it handed over half a capture as though it were all of it")
	}
}

// TestTracesStayQuietWhenTheCaptureIsWhole: nothing about the platform belongs
// in an answer that has everything.
func TestTracesStayQuietWhenTheCaptureIsWhole(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	s.traces = newTraceRing()
	s.traces.add("stdout", "something the game printed", 1)

	_, out, err := s.tracesTool(ctx, nil, tracesInput{})
	if err != nil {
		t.Fatalf("game_traces: %v", err)
	}

	if len(out.Lines) == 0 {
		t.Fatal("the line went missing")
	}
	if out.Unavailable != "" {
		t.Errorf("it explained itself with a whole capture in hand: %q", out.Unavailable)
	}
}
