package ebitenmcp

import (
	"sync"
	"time"
)

// FrameTiming is what one tick cost.
type FrameTiming struct {
	Tick   int64 `json:"tick"`
	Update int64 `json:"update_ns"`
	Draw   int64 `json:"draw_ns"`
	Wall   int64 `json:"wall_ns"`
}

// frameTimingHistory is ten seconds at sixty ticks a second: long enough to
// cover a stutter somebody noticed and went to look at, short enough to be a
// fixed array rather than something that grows.
const frameTimingHistory = 600

// frameTimings is the ring of what recent ticks cost.
//
// Its own type with its own lock because it has nothing to do with anything else
// the runtime holds: three methods write to it, one reads, and none of them
// needs to know whether the game is paused or what it is drawing. Under the
// runtime's mutex it was four more fields on a lock already held by the tick
// counter, the command queue, the capture list and half a dozen handles — which
// is how a mutex stops meaning anything.
type frameTimings struct {
	mu sync.Mutex

	frames [frameTimingHistory]FrameTiming
	n      int
}

// add records a completed tick. Draw is the previous frame's, because a tick's
// own draw has not happened yet when its update finishes.
func (t *frameTimings) add(tick int64, update, draw, wall time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.frames[t.n%frameTimingHistory] = FrameTiming{
		Tick:   tick,
		Update: update.Nanoseconds(),
		Draw:   draw.Nanoseconds(),
		Wall:   wall.Nanoseconds(),
	}
	t.n++
}

// setDraw fills in the draw half of the current tick. Draw runs after Update, so
// the entry already exists by the time this is called.
func (t *frameTimings) setDraw(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.n == 0 {
		return
	}
	t.frames[(t.n-1)%frameTimingHistory].Draw = d.Nanoseconds()
}

// addDraw adds to it, for the work that happens after Draw returns: the game's
// final pass, and the copy a capture of it needs. Keeping it in the same number
// is what lets game_frametimes answer "is watching this costing me anything".
func (t *frameTimings) addDraw(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.n == 0 {
		return
	}
	t.frames[(t.n-1)%frameTimingHistory].Draw += d.Nanoseconds()
}

// recent returns the timings it has, oldest first.
func (t *frameTimings) recent() []FrameTiming {
	t.mu.Lock()
	defer t.mu.Unlock()

	n := min(t.n, frameTimingHistory)

	out := make([]FrameTiming, 0, n)
	for i := t.n - n; i < t.n; i++ {
		out = append(out, t.frames[i%frameTimingHistory])
	}
	return out
}
