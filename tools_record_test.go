package ebitenmcp

import (
	"os"
	"strings"
	"testing"
)

// game_record and game_profile, the two tools nobody tested because they are
// the two that cost something to run.
//
// Both are cheap when asked to be. A recording of a handful of frames with no
// video is a fraction of a second, and a heap profile is immediate — it is the
// default cpu one that samples for five seconds of wall clock, which is why
// nothing here asks for it. What that leaves untested is the waiting, and the
// waiting is the part least likely to be wrong.

// TestRecordToolCoversTheTicksItSaysItDoes. The answer is mostly numbers about
// frames somebody is not looking at, and the numbers are how they decide
// whether to look: a recording that says it covered forty ticks and covered two
// sends an agent hunting for a bug in the wrong place.
func TestRecordToolCoversTheTicksItSaysItDoes(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, out, err := s.record(ctx, nil, recordInput{Frames: 6, NoVideo: true, Stage: "offscreen"})
	if err != nil {
		t.Fatalf("game_record: %v", err)
	}

	if out.Frames != 6 {
		t.Errorf("it kept %d frames, want 6", out.Frames)
	}
	if out.Stage != StageOffscreen {
		t.Errorf("it recorded the %s stage, want offscreen", out.Stage)
	}
	if out.LastTick < out.FirstTick {
		t.Errorf("it ran from tick %d to %d, which is backwards", out.FirstTick, out.LastTick)
	}
	if out.TicksCovered != out.LastTick-out.FirstTick {
		t.Errorf("it says it covered %d ticks between %d and %d",
			out.TicksCovered, out.FirstTick, out.LastTick)
	}

	// The contact sheet is the part that comes back inline, so it is the part
	// an agent actually sees. A recording with no picture is a report.
	if out.Sheet == nil {
		t.Fatal("it produced no contact sheet")
	}
	if _, err := os.Stat(out.Sheet.Path); err != nil {
		t.Errorf("it says the sheet is at %s and it is not: %v", out.Sheet.Path, err)
	}
	if out.Video != nil {
		t.Errorf("no_video was asked for and it wrote %s anyway", out.Video.Path)
	}
}

// TestRecordToolKeepsOneFrameOutOfEvery is what makes a long stretch of game
// affordable, and the two numbers do not mean the same thing: frames is how
// many the recorder goes past, every is how many of those it keeps. So twenty
// at one in five is four kept, over five times the ticks of four at one in one.
// A tool that took every and ignored it would cost twenty captures — each one a
// synchronisation with the GPU and a full-resolution allocation — to answer the
// cheap question.
func TestRecordToolKeepsOneFrameOutOfEvery(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, dense, err := s.record(ctx, nil, recordInput{Frames: 4, NoVideo: true, Stage: "offscreen"})
	if err != nil {
		t.Fatalf("game_record: %v", err)
	}

	_, sparse, err := s.record(ctx, nil, recordInput{Frames: 20, Every: 5, NoVideo: true, Stage: "offscreen"})
	if err != nil {
		t.Fatalf("game_record with every: %v", err)
	}

	if sparse.Frames != dense.Frames {
		t.Errorf("twenty frames one in five kept %d and four one in one kept %d; both are four",
			sparse.Frames, dense.Frames)
	}
	if sparse.TicksCovered <= dense.TicksCovered {
		t.Errorf("the same four frames covered %d ticks one in five and %d ticks one in one",
			sparse.TicksCovered, dense.TicksCovered)
	}
}

func TestRecordToolWritesTheVideoWhenAskedFor(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// gif rather than mp4, because it is the one that can be produced with or
	// without ffmpeg, so this runs the same everywhere.
	_, out, err := s.record(ctx, nil, recordInput{Frames: 4, Format: "gif", Stage: "offscreen"})
	if err != nil {
		t.Fatalf("game_record: %v", err)
	}

	if out.Video == nil {
		t.Fatal("it recorded nothing to watch")
	}
	if out.Video.Kind != "gif" {
		t.Errorf("it wrote a %s, want a gif", out.Video.Kind)
	}
	if out.Video.Bytes == 0 {
		t.Errorf("the video at %s is empty", out.Video.Path)
	}
}

// TestRecordToolCanReturnTheAnimationItself.
//
// Asked for rather than assumed, because it is a guess about the client:
// where one animates a gif it beats a grid of stills at showing motion, and
// where one does not it shows a single frame, and then the contact sheet was
// the better answer all along. So the tool has to hand back the gif when it
// can and say why it could not when it cannot — never quietly return the sheet
// as though that had been the question.
func TestRecordToolCanReturnTheAnimationItself(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	result, _, err := s.record(ctx, nil, recordInput{
		Frames: 4, Format: "gif", Inline: "gif", Stage: "offscreen",
	})
	if err != nil {
		t.Fatalf("game_record: %v", err)
	}

	image, note := splitContent(t, result)
	if image.MIMEType != "image/gif" {
		t.Errorf("it returned a %s inline, and a gif was asked for", image.MIMEType)
	}
	if len(image.Data) == 0 {
		t.Error("the inline animation is empty")
	}
	if !strings.Contains(note, "video:") {
		t.Errorf("the note does not say where the file went: %q", note)
	}

	// Asking for an mp4 inline as a gif cannot work, and the answer is the
	// contact sheet plus the reason — not a failed call over the part of the
	// request that was a preference.
	result, _, err = s.record(ctx, nil, recordInput{
		Frames: 4, Format: "mp4", Inline: "gif", Stage: "offscreen",
	})
	if err != nil {
		t.Fatalf("game_record: %v", err)
	}
	if _, note = splitContent(t, result); !strings.Contains(note, "no gif") {
		t.Errorf("it fell back to the sheet without saying why: %q", note)
	}
}

// TestProfileToolAnswersWithSomethingReadable.
//
// A pprof file is addresses, not names, so the artifact on its own is no use to
// whoever asked. The summary is the tool — the file run back through the Go
// toolchain against this binary — and it comes back as the reply's text rather
// than in the structured answer, which is worth knowing before looking for it
// in the wrong half.
func TestProfileToolAnswersWithSomethingReadable(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// heap, not cpu: cpu samples for five seconds of wall clock and holds the
	// process-global profiler while it does.
	result, out, err := s.profile(ctx, nil, profileInput{Kind: "heap", Lines: 5})
	if err != nil {
		t.Fatalf("game_profile: %v", err)
	}

	if out.Kind != "heap" {
		t.Errorf("it profiled %q", out.Kind)
	}
	if out.Artifact == nil {
		t.Fatal("it wrote no profile")
	}
	if _, err := os.Stat(out.Artifact.Path); err != nil {
		t.Errorf("it says the profile is at %s and it is not: %v", out.Artifact.Path, err)
	}
	if out.Note != "" {
		t.Errorf("Note carries the reason there is no summary, and there is one: %q", out.Note)
	}

	_, summary := textOf(result)
	if summary == "" {
		t.Fatal("it returned a file of addresses and nothing anybody can read")
	}
	// A pprof top summary names its units and the profile it came from; an
	// empty heap would print a header and nothing else, which is still an
	// answer, but a blank line is not.
	if !strings.Contains(summary, out.Artifact.Path) {
		t.Errorf("the summary does not say which file it came from: %q", summary)
	}
}

func TestProfileToolRefusesAKindThatIsNotOne(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, _, err := s.profile(ctx, nil, profileInput{Kind: "vibes"})
	if err == nil {
		t.Fatal("it accepted a profile that does not exist")
	}
	// Naming the ones that do work is the difference between an error somebody
	// can act on and one that sends them to the source.
	for _, want := range []string{"cpu", "heap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %s: %v", want, err)
		}
	}
}
