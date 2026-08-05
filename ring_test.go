package ebitenmcp

import (
	"image"
	"testing"
	"time"
)

// The buffer's queue is claimed before a frame is read back rather than after,
// so these are about the token that stands for that claim. Getting it wrong is
// not a dropped frame: offer runs on the game's draw, so a token that cannot be
// handed back stops the game.

func TestRingDisableDoesNotStrandTheDrawThatIsMidOffer(t *testing.T) {
	ring := newFrameRing()
	ring.Enable(4<<20, 1, StageOffscreen)

	if !ring.wants() {
		t.Fatal("a fresh buffer refused the first frame")
	}

	// Disabled between the claim and the hand-off, which is the ordering a
	// game_frames {"disable": true} arriving mid-frame produces. offer must
	// return; when it read from a channel Disable had just set to nil, it did
	// not, and the game stopped drawing.
	ring.Disable()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ring.offer(image.NewRGBA(image.Rect(0, 0, 4, 4)), 1)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("offer never returned after the buffer was disabled under it")
	}
}

func TestRingKeepsSamplingAfterADroppedFrame(t *testing.T) {
	ring := newFrameRing()
	ring.Enable(4<<20, 1, StageOffscreen)
	defer ring.Disable()

	// Claim every slot and never hand one back, which is what a stalled encoder
	// looks like. The buffer must refuse rather than block, and must recover
	// once the slots come back.
	for i := 0; i < ringQueueDepth; i++ {
		if !ring.wants() {
			t.Fatalf("refused frame %d while there was still room", i)
		}
	}
	if ring.wants() {
		t.Error("accepted a frame with no room for it")
	}

	if status := ring.Status(); status["dropped"].(int64) == 0 {
		t.Error("a refused frame was not counted as dropped")
	}
}

// wantsNext is what decides whether a frame is worth forcing into existence, so
// it must follow `every` and must not consume anything.
func TestRingWantsNextFollowsEveryWithoutConsuming(t *testing.T) {
	ring := newFrameRing()
	ring.Enable(4<<20, 3, StageOffscreen)
	defer ring.Disable()

	for i := 0; i < 3; i++ {
		if !ring.wantsNext() {
			t.Fatal("peeking changed the answer")
		}
	}

	ring.wants() // consumes the first of three
	if ring.wantsNext() {
		t.Error("wants one of every three, and wanted two in a row")
	}
}
