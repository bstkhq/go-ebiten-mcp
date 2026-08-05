package ebitenmcp

import "testing"

// Two pieces of the capture queue that only run when something has gone wrong,
// and so had never run at all: the timed-out caller's way off the queue, and
// the fallback that keeps a missing final pass from becoming a hang. Both are
// plain data, which is the point — they are testable without a display, and
// there was no reason for them to be the only untested part of the path.

func TestCancelRemovesOnlyTheCallerThatLeft(t *testing.T) {
	var c captures

	first := make(chan *Frame, 1)
	second := make(chan *Frame, 1)
	third := make(chan *Frame, 1)

	for _, ch := range []chan *Frame{first, second, third} {
		c.add(captureRequest{ch: ch, stage: StageOffscreen})
	}

	c.cancel(second)

	if got := c.pending(); got != 2 {
		t.Fatalf("%d waiting after one cancelled, want 2", got)
	}

	// The one that left must not be served, and the other two must be: a
	// cancel that took the wrong request off would leave a caller waiting out
	// its whole timeout for a frame that was handed to somebody who had gone.
	offscreen, _ := c.take()
	if len(offscreen) != 2 {
		t.Fatalf("take gave %d channels, want 2", len(offscreen))
	}
	for _, ch := range offscreen {
		if ch == second {
			t.Error("the cancelled caller is still on the queue")
		}
	}
}

func TestCancelIgnoresAChannelThatWasNeverThere(t *testing.T) {
	var c captures

	waiting := make(chan *Frame, 1)
	c.add(captureRequest{ch: waiting, stage: StageFinal})

	c.cancel(make(chan *Frame, 1))

	if got := c.pending(); got != 1 {
		t.Fatalf("%d waiting, want the one that was never cancelled", got)
	}
}

// TestDemoteToOffscreenLeavesNobodyWaiting covers the fallback in
// keepFinalPassAlive: Ebitengine has stopped compositing, the transparent-pixel
// nudge has not brought it back, and the choice is between an offscreen frame
// labelled as one and a caller that waits out its timeout and is then told the
// game loop had stopped — while the loop is running perfectly.
func TestDemoteToOffscreenLeavesNobodyWaiting(t *testing.T) {
	final := make(chan *Frame, 1)
	offscreen := make(chan *Frame, 1)

	plan := capturePlan{
		offscreen: []chan *Frame{offscreen},
		final:     []chan *Frame{final},
		ring:      StageFinal,
	}
	plan.demoteToOffscreen()

	if plan.wantsFinal() {
		t.Error("the plan still wants a final screen that is not going to be drawn")
	}
	if len(plan.offscreen) != 2 {
		t.Fatalf("%d callers to serve from the offscreen, want both", len(plan.offscreen))
	}
	if plan.ring != StageOffscreen {
		t.Errorf("the buffer is still set to %s", plan.ring)
	}
}

// TestDemoteToOffscreenLeavesTheBufferAloneWhenItAskedForOne is the other half:
// a buffer already sampling the offscreen has nothing to demote, and turning
// its stage into a copy of whatever the waiters asked for would change what it
// records because somebody took a screenshot.
func TestDemoteToOffscreenLeavesTheBufferAloneWhenItAskedForOne(t *testing.T) {
	plan := capturePlan{ring: StageOffscreen}
	plan.demoteToOffscreen()

	if plan.ring != StageOffscreen {
		t.Errorf("the buffer's stage became %s", plan.ring)
	}

	// And a plan nobody is waiting on stays one nobody is waiting on.
	empty := capturePlan{}
	empty.demoteToOffscreen()

	if empty.wantsOffscreen() || empty.wantsFinal() {
		t.Error("demoting an empty plan invented work for the frame")
	}
}
