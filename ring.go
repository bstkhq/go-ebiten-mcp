package ebitenmcp

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"sync"
	"time"
)

// The retrospective buffer: the frames from *before* something happened.
//
// game_record grabs what comes next, which is only useful for a problem you
// already know how to reproduce. The interesting ones are the other kind — the
// crash you were not expecting, the flicker you saw once — and for those the
// frames that matter are already in the past by the time anybody asks.
//
// Keeping them costs, which is why this is off by default and why the cost is
// reported rather than absorbed quietly:
//
//   - Reading pixels back is a synchronisation point with the GPU, so every
//     frame kept is time taken from the game.
//   - A 1080p frame is 8 MB raw. Three seconds at sixty frames a second would be
//     1.4 GB, so frames are encoded and the buffer is bounded by memory rather
//     than by a count, since a count means nothing without a resolution.
//
// Encoding happens on its own goroutine behind a small queue. When the encoder
// falls behind, frames are dropped and counted: a buffer quietly keeping half of
// what it was asked to keep would be worse than one that says so.

const (
	defaultRingBudget = 64 << 20 // bytes
	ringQueueDepth    = 4
	ringJPEGQuality   = 85
)

// ringFrame is one kept frame, already encoded.
type ringFrame struct {
	Tick int64
	Time time.Time
	Data []byte
}

type frameRing struct {
	mu sync.Mutex

	enabled bool
	every   int
	budget  int
	stage   Stage

	frames []ringFrame
	bytes  int

	seen    int64 // frames offered since it was enabled
	dropped int64
	encoded int64

	queue chan pendingFrame
	stop  chan struct{}
	done  chan struct{}
}

// pendingFrame is a frame on its way to the encoder. It carries its tick rather
// than having the encoder ask for one later: by then the game has moved on, and
// a frame labelled with the wrong moment is worse than one with no label.
type pendingFrame struct {
	image *image.RGBA
	tick  int64
	when  time.Time
}

func newFrameRing() *frameRing {
	return &frameRing{every: 1, budget: defaultRingBudget, stage: StageOffscreen}
}

// Stage is which stage the ring keeps.
//
// The offscreen, unless asked otherwise, and that default is about cost rather
// than fidelity: this is the one thing here that captures continuously, and on a
// game with a final pass the difference is the logical resolution against the
// window's — thirteen times the bytes off the GPU and through the encoder, for
// every frame, for as long as it is on. A screenshot pays that once; a buffer
// running for ten minutes pays it thirty-six thousand times.
func (r *frameRing) Stage() Stage {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.stage
}

// Enable starts keeping frames, replacing whatever was kept before.
func (r *frameRing) Enable(budget, every int, stage Stage) {
	r.Disable()

	if budget <= 0 {
		budget = defaultRingBudget
	}
	if every <= 0 {
		every = 1
	}
	if stage != StageFinal {
		stage = StageOffscreen
	}

	r.mu.Lock()
	r.enabled, r.budget, r.every, r.stage = true, budget, every, stage
	r.frames, r.bytes = nil, 0
	r.seen, r.dropped, r.encoded = 0, 0, 0
	r.queue = make(chan pendingFrame, ringQueueDepth)
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	queue, stop, done := r.queue, r.stop, r.done
	r.mu.Unlock()

	go r.encode(queue, stop, done)
}

// Disable stops keeping frames and lets go of the ones it had.
func (r *frameRing) Disable() {
	r.mu.Lock()
	if !r.enabled {
		r.mu.Unlock()
		return
	}

	r.enabled = false
	stop, done := r.stop, r.done
	r.frames, r.bytes = nil, 0
	r.mu.Unlock()

	close(stop)
	<-done
}

// wants reports whether this frame should be kept, and is deliberately cheap:
// it runs inside Draw on every single frame.
func (r *frameRing) wants() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.enabled {
		return false
	}

	keep := r.seen%int64(r.every) == 0
	r.seen++
	return keep
}

// offer hands a frame to the encoder, or drops it.
//
// Dropping rather than blocking is the whole point: waiting here would stall the
// game's draw on a JPEG encoder, which is exactly the sort of help nobody asked
// for.
func (r *frameRing) offer(img *image.RGBA, tick int64) {
	r.mu.Lock()
	queue := r.queue
	enabled := r.enabled
	r.mu.Unlock()

	if !enabled || queue == nil {
		return
	}

	select {
	case queue <- pendingFrame{image: img, tick: tick, when: time.Now()}:
	default:
		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
	}
}

func (r *frameRing) encode(queue chan pendingFrame, stop, done chan struct{}) {
	defer close(done)

	for {
		select {
		case <-stop:
			return
		case pending := <-queue:
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, pending.image, &jpeg.Options{Quality: ringJPEGQuality}); err != nil {
				continue
			}
			r.keep(ringFrame{Tick: pending.tick, Time: pending.when, Data: buf.Bytes()})
		}
	}
}

func (r *frameRing) keep(frame ringFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.enabled {
		return
	}

	r.frames = append(r.frames, frame)
	r.bytes += len(frame.Data)
	r.encoded++

	// Evict oldest first until it fits. By memory, not by count: a hundred
	// frames means nothing without knowing how big they are.
	for r.bytes > r.budget && len(r.frames) > 1 {
		r.bytes -= len(r.frames[0].Data)
		r.frames = r.frames[1:]
	}
}

// Frames returns the last n kept frames, oldest first.
func (r *frameRing) Frames(n int) []ringFrame {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n <= 0 || n > len(r.frames) {
		n = len(r.frames)
	}

	out := make([]ringFrame, n)
	copy(out, r.frames[len(r.frames)-n:])
	return out
}

// Status is what game_state reports, so a buffer throwing away half of what it
// is given is visible rather than something to be deduced from tick numbers.
func (r *frameRing) Status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.enabled {
		return map[string]any{"enabled": false}
	}

	status := map[string]any{
		"enabled":   true,
		"frames":    len(r.frames),
		"memory_mb": round2(float64(r.bytes) / (1 << 20)),
		"budget_mb": round2(float64(r.budget) / (1 << 20)),
		"every":     r.every,
		"stage":     string(r.stage),
		"encoded":   r.encoded,
		"dropped":   r.dropped,
	}

	if len(r.frames) > 0 {
		status["oldest_tick"] = r.frames[0].Tick
		status["newest_tick"] = r.frames[len(r.frames)-1].Tick
	}
	if r.dropped > 0 {
		status["note"] = fmt.Sprintf("the encoder could not keep up with %d frames; "+
			"raise every to sample less often", r.dropped)
	}
	return status
}

// decode turns a kept frame back into pixels, for the contact sheet.
func (f ringFrame) decode() (*image.RGBA, error) {
	img, err := jpeg.Decode(bytes.NewReader(f.Data))
	if err != nil {
		return nil, err
	}
	return toRGBA(img), nil
}
