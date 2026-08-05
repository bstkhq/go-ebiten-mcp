package ebitenmcp

import (
	"context"
	"image"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// Frame is one captured screen, at the game's own resolution and untouched.
type Frame struct {
	Tick  int64       `json:"tick"`
	Time  time.Time   `json:"time"`
	Image *image.RGBA `json:"-"`
}

// Capture asks for the next frame the game draws.
//
// Reading pixels back is a synchronisation point with the GPU, so it only
// happens when somebody is waiting for one. Capturing every frame
// unconditionally would cost the game a large part of its frame budget for
// images nobody looks at.
func (r *Runtime) Capture(ctx context.Context) (*Frame, error) {
	ch := make(chan *Frame, 1)

	r.mu.Lock()
	r.captures = append(r.captures, ch)
	r.mu.Unlock()

	select {
	case f := <-ch:
		return f, nil
	case <-ctx.Done():
		r.cancelCapture(ch)
		return nil, ErrLoopStalled
	}
}

func (r *Runtime) cancelCapture(ch chan *Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, c := range r.captures {
		if c == ch {
			r.captures = append(r.captures[:i], r.captures[i+1:]...)
			return
		}
	}
}

// LastFrame is the most recently captured frame, or nil if nothing has been
// captured yet. Note that this is the last frame somebody asked for, not the
// last frame drawn: nothing is captured behind the caller's back.
func (r *Runtime) LastFrame() *Frame {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.lastFrame
}

// serveCaptures hands the frame just drawn to everyone waiting for one. Called
// at the end of Draw, when the screen holds exactly what the player sees.
func (r *Runtime) serveCaptures(screen *ebiten.Image) {
	r.mu.Lock()
	waiting := r.captures
	r.captures = nil
	ring := r.ring
	// A crashed game keeps drawing — the wrapper paints the last frame and the
	// panic over it — and a paused one redraws the same thing forever. Feeding
	// either to the buffer fills it with identical frames that evict exactly the
	// ones somebody is about to ask for, which is the opposite of the job.
	frozen := r.crash != nil || (r.paused && r.steps == 0)
	r.mu.Unlock()

	keepForRing := ring != nil && !frozen && ring.wants()
	if len(waiting) == 0 && !keepForRing {
		return
	}

	// One read for both. ReadPixels is a synchronisation point with the GPU, so
	// doing it twice on a frame somebody asked for while the ring is running
	// would cost the game twice for the same pixels.
	frame := r.readFrame(screen)

	for _, ch := range waiting {
		ch <- frame
	}
	if keepForRing {
		ring.offer(frame.Image, frame.Tick)
	}
}

// Ring is the retrospective frame buffer, which is off until something asks for
// it.
func (r *Runtime) Ring() *frameRing {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ring == nil {
		r.ring = newFrameRing()
	}
	return r.ring
}

// keepFrame captures the screen without anyone having asked, which is only
// worth its cost when the game has just crashed and this is the last image
// that will ever exist of it.
func (r *Runtime) keepFrame(screen *ebiten.Image) {
	r.readFrame(screen)
}

func (r *Runtime) readFrame(screen *ebiten.Image) *Frame {
	bounds := screen.Bounds()

	img := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	screen.ReadPixels(img.Pix)

	frame := &Frame{
		Tick:  r.Tick(),
		Time:  time.Now(),
		Image: img,
	}

	r.mu.Lock()
	r.lastFrame = frame
	r.mu.Unlock()

	return frame
}
