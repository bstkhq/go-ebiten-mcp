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
	// lifecycle serialises Enable and Disable end to end; mu guards the fields.
	lifecycle sync.Mutex

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

	// slots is the encoder queue's capacity, held as tokens so that a frame can
	// be refused before it is read back rather than after.
	slots chan struct{}

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

// Enabled reports whether frames are being kept.
func (r *frameRing) Enabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.enabled
}

// Stage is which stage the ring keeps.
//
// The offscreen, unless asked otherwise, and that default is about cost rather
// than fidelity: this is the one thing here that captures continuously, and on a
// game with a final pass the difference is the logical resolution against the
// window's. That is the scale factor squared — four times the pixels at double
// size, around thirteen for a 480x320 game filling a 1080p screen — off the GPU
// and through the encoder, for every frame, for as long as it is on. A
// screenshot pays it once; a buffer running for ten minutes pays it thirty-six
// thousand times.
func (r *frameRing) Stage() Stage {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.stage
}

// Enable starts keeping frames, replacing whatever was kept before.
//
// Serialised against itself and against Disable. Two enables arriving together
// could each see nothing running, each install their own channels, and each
// start an encoder — one of which would then sit for the life of the process on
// a stop that will never be closed, holding a queue of full-resolution frames.
// MCP calls are concurrent, so "two at once" is a client being ordinary.
func (r *frameRing) Enable(budget, every int, stage Stage) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()

	r.disable()

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
	r.slots = make(chan struct{}, ringQueueDepth)
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	queue, slots, stop, done := r.queue, r.slots, r.stop, r.done
	r.mu.Unlock()

	go r.encode(queue, slots, stop, done)
}

// Disable stops keeping frames and lets go of the ones it had.
func (r *frameRing) Disable() {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()

	r.disable()
}

// disable is the body, for callers that already hold lifecycle.
func (r *frameRing) disable() {
	r.mu.Lock()
	if !r.enabled {
		r.mu.Unlock()
		return
	}

	r.enabled = false
	stop, done, queue := r.stop, r.done, r.queue
	r.frames, r.bytes = nil, 0
	r.queue, r.slots = nil, nil
	r.mu.Unlock()

	close(stop)
	<-done

	// Whatever the encoder never got to. Without this the queue and up to four
	// full-resolution frames stayed referenced by a ring that reports itself as
	// holding nothing.
	for len(queue) > 0 {
		<-queue
	}
}

// wants reports whether this frame should be kept, and is deliberately cheap:
// it runs inside Draw on every single frame.
//
// It also claims the slot in the encoder's queue, before the caller pays for the
// frame. Deciding afterwards — reading the pixels back, allocating the image,
// and only then discovering the queue was full — meant a dropped frame had
// already cost a synchronisation with the GPU and eight megabytes at 1080p. The
// point of dropping is not to pay.
func (r *frameRing) wants() bool {
	r.mu.Lock()

	if !r.enabled {
		r.mu.Unlock()
		return false
	}

	keep := r.seen%int64(r.every) == 0
	r.seen++
	queue := r.queue
	r.mu.Unlock()

	if !keep || queue == nil {
		return false
	}

	select {
	case r.slots <- struct{}{}:
		return true
	default:
		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
		return false
	}
}

// wantsNext peeks at whether the next frame offered would be kept, without
// claiming it. Used to decide whether a frame is worth forcing into existence at
// all; wants is what actually consumes one.
func (r *frameRing) wantsNext() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.enabled && r.seen%int64(r.every) == 0
}

// offer hands a frame to the encoder.
//
// It cannot block and it cannot drop: wants already claimed the slot this frame
// is going into, which is why the decision is made there and not here.
func (r *frameRing) offer(img *image.RGBA, tick int64) {
	r.mu.Lock()
	queue, slots, enabled := r.queue, r.slots, r.enabled
	r.mu.Unlock()

	// Both taken under the lock and both checked before use. Disable clears them
	// while a frame may already be on its way here, and a receive from a nil
	// channel blocks for ever — which on this path is the game's draw, so the
	// game would stop drawing because somebody turned the buffer off.
	if !enabled || queue == nil {
		releaseSlot(slots)
		return
	}

	select {
	case queue <- pendingFrame{image: img, tick: tick, when: time.Now()}:
	default:
		// Only reachable if the ring was restarted between wants and here.
		releaseSlot(slots)

		r.mu.Lock()
		r.dropped++
		r.mu.Unlock()
	}
}

// releaseSlot hands a claimed slot back, if there is still a ring holding them.
// Never blocks: a slot nobody is waiting for is not worth stopping a frame over.
func releaseSlot(slots chan struct{}) {
	if slots == nil {
		return
	}
	select {
	case <-slots:
	default:
	}
}

func (r *frameRing) encode(queue chan pendingFrame, slots, stop, done chan struct{}) {
	defer close(done)

	for {
		select {
		case <-stop:
			return
		case pending := <-queue:
			var buf bytes.Buffer
			err := jpeg.Encode(&buf, pending.image, &jpeg.Options{Quality: ringJPEGQuality})

			// The slot goes back whether or not the encode worked, or the buffer
			// would quietly shrink its own capacity one failure at a time. Taken
			// as an argument rather than read from the struct, because Disable
			// clears the field while this goroutine may still be running.
			releaseSlot(slots)

			if err != nil {
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
