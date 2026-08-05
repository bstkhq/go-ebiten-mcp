package ebitenmcp

import (
	"context"
	"image"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// Stage is which of Ebitengine's two drawing steps a frame was read from.
//
// Ebitengine draws twice. First the game's Draw fills an offscreen at the
// logical resolution. Then, if the game implements ebiten.FinalScreenDrawer, its
// DrawFinalScreen composites that offscreen onto the real screen — and a CRT
// shader, scanlines, a letterbox or a custom scaling filter all live in that
// second step. Reading the offscreen would show the image before any of it,
// which is not what the player is looking at.
type Stage string

const (
	// StageOffscreen is the image the game's own Draw produced, at the logical
	// resolution.
	StageOffscreen Stage = "offscreen"

	// StageFinal is what the player sees, after the game's own final pass, at
	// the resolution of the window.
	StageFinal Stage = "final"
)

// Frame is one captured screen, untouched.
type Frame struct {
	Tick  int64       `json:"tick"`
	Time  time.Time   `json:"time"`
	Stage Stage       `json:"stage"`
	Image *image.RGBA `json:"-"`
}

// Capture asks for the next frame the game draws, at whichever stage this
// runtime is configured for.
//
// Reading pixels back is a synchronisation point with the GPU, so it only
// happens when somebody is waiting for one. Capturing every frame
// unconditionally would cost the game a large part of its frame budget for
// images nobody looks at.
func (r *Runtime) Capture(ctx context.Context) (*Frame, error) {
	return r.CaptureStage(ctx, r.DefaultStage())
}

// CaptureStage asks for the next frame at a particular stage.
//
// Asking for the offscreen of a game that has a final pass is how you tell
// whether a visual bug is in the game or in the pass — and it is what tests
// should use, since the final screen is the size of the window and a golden that
// changes with the monitor is no golden at all.
func (r *Runtime) CaptureStage(ctx context.Context, stage Stage) (*Frame, error) {
	ch := make(chan *Frame, 1)

	r.mu.Lock()
	r.captures = append(r.captures, captureRequest{ch: ch, stage: stage})
	r.mu.Unlock()

	select {
	case f := <-ch:
		return f, nil
	case <-ctx.Done():
		r.cancelCapture(ch)
		return nil, ErrLoopStalled
	}
}

// HasFinalPass reports whether the game draws its own final screen, which is
// what makes the two stages different images.
func (r *Runtime) HasFinalPass() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.hasFinal
}

// DefaultStage is the stage Capture uses when nobody asks for one.
func (r *Runtime) DefaultStage() Stage {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.defaultStage
}

func (r *Runtime) cancelCapture(ch chan *Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, c := range r.captures {
		if c.ch == ch {
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

// captureRequest is one waiter and the stage it asked for.
type captureRequest struct {
	ch    chan *Frame
	stage Stage
}

// capturePlan is what this frame owes, decided once before anything is drawn.
//
// It has to be decided up front because reading the final screen is not
// possible: ebiten.FinalScreen has no ReadPixels. The only way to see it is to
// hand the game an image we own instead, and that swap must happen before the
// game draws into it.
type capturePlan struct {
	offscreen []chan *Frame
	final     []chan *Frame

	// stage is "" when the buffer does not want this frame; buffer is the one
	// that did, carried here so that finishCapture does not have to go and ask
	// for it again on every frame it keeps.
	ring   Stage
	buffer *frameRing
}

func (p capturePlan) wantsFinal() bool {
	return len(p.final) > 0 || p.ring == StageFinal
}

func (p capturePlan) wantsOffscreen() bool {
	return len(p.offscreen) > 0 || p.ring == StageOffscreen
}

// demoteToOffscreen settles for the offscreen for everyone, because the final
// screen is not going to be drawn this frame and somebody is waiting. The
// frames go out labelled offscreen, so what arrives is at least true.
func (p *capturePlan) demoteToOffscreen() {
	p.offscreen = append(p.offscreen, p.final...)
	p.final = nil

	if p.ring == StageFinal {
		p.ring = StageOffscreen
	}
}

// captureWanted reports whether anything would be done with a frame drawn right
// now. Deliberately cheap and free of side effects: it runs inside Draw on
// every frame, and unlike beginCapture it must not consume anything, because
// the frame it is asked about has not been drawn yet.
func (r *Runtime) captureWanted() bool {
	r.mu.Lock()
	waiting := len(r.captures)
	ring := r.ring
	r.mu.Unlock()

	if waiting > 0 {
		return true
	}

	// The buffer being on is not enough. With `every: 60` it keeps one frame in
	// sixty, and forcing the final pass on the other fifty-nine turns off the
	// engine's own optimisation for a picture that is not changing — and runs
	// the game's shader — for frames nobody will ever look at.
	return ring != nil && ring.wantsNext()
}

// beginCapture decides what this frame owes, and takes the waiters off the
// queue so the next frame does not serve them twice.
//
// It is also the single place the ring's frame counter advances, which is why
// it must be called exactly once per drawn frame.
func (r *Runtime) beginCapture() capturePlan {
	r.mu.Lock()
	waiting := r.captures
	r.captures = nil
	ring := r.ring
	hasFinal := r.hasFinal
	// A crashed game keeps drawing — the wrapper paints the last frame and the
	// panic over it — and a paused one redraws the same thing forever. Feeding
	// either to the buffer fills it with identical frames that evict exactly the
	// ones somebody is about to ask for, which is the opposite of the job.
	frozen := r.crash != nil || (r.paused && r.steps == 0)
	r.mu.Unlock()

	var plan capturePlan

	for _, req := range waiting {
		// With no final pass of its own the game's offscreen is what the player
		// sees, give or take Ebitengine's scaling, so both stages are the same
		// image and there is nothing to read twice.
		if req.stage == StageFinal && hasFinal {
			plan.final = append(plan.final, req.ch)
			continue
		}
		plan.offscreen = append(plan.offscreen, req.ch)
	}

	if ring != nil && !frozen && ring.wants() {
		plan.ring, plan.buffer = ring.Stage(), ring
		if plan.ring == StageFinal && !hasFinal {
			plan.ring = StageOffscreen
		}
	}

	return plan
}

// finishCapture reads back whichever stages this frame owed and hands the
// frames out.
//
// Each stage is read at most once however many people asked for it: ReadPixels
// is a synchronisation point with the GPU, and paying for it twice for the same
// pixels is exactly the sort of overhead a debugging tool should not add.
func (r *Runtime) finishCapture(plan capturePlan, offscreen, final *ebiten.Image) {
	var offFrame, finalFrame *Frame

	if plan.wantsOffscreen() {
		offFrame = readFrame(offscreen, r.Tick(), StageOffscreen)
		for _, ch := range plan.offscreen {
			ch <- offFrame
		}
	}

	if plan.wantsFinal() && final != nil {
		finalFrame = readFrame(final, r.Tick(), StageFinal)
		for _, ch := range plan.final {
			ch <- finalFrame
		}
	}

	// The offscreen is preferred for lastFrame because that is what redraws the
	// screen after a crash, and it is the only one whose size matches it.
	last := offFrame
	if last == nil {
		last = finalFrame
	}

	r.mu.Lock()
	if last != nil {
		r.lastFrame = last
	}
	r.mu.Unlock()

	if plan.buffer == nil {
		return
	}

	frame := offFrame
	if plan.ring == StageFinal {
		frame = finalFrame
	}
	if frame != nil {
		plan.buffer.offer(frame.Image, frame.Tick)
	}
}

// Ring is the retrospective frame buffer, building it on the first ask.
//
// It is a constructor as much as a getter, which matters because game_state
// reports the buffer's status: merely asking what the game is doing brings the
// buffer into being. Empty and disabled, so it costs an allocation and nothing
// else, but it is why nothing downstream needs to handle a nil one.
func (r *Runtime) Ring() *frameRing {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ring == nil {
		r.ring = newFrameRing()
	}
	return r.ring
}

// keepFrame captures the screen without anyone having asked, which is only
// worth its cost when the game has just crashed and this is the last image that
// will ever exist of it.
func (r *Runtime) keepFrame(screen *ebiten.Image) {
	frame := readFrame(screen, r.Tick(), StageOffscreen)

	r.mu.Lock()
	r.lastFrame = frame
	r.mu.Unlock()
}

func readFrame(screen *ebiten.Image, tick int64, stage Stage) *Frame {
	bounds := screen.Bounds()

	img := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	screen.ReadPixels(img.Pix)

	return &Frame{
		Tick:  tick,
		Time:  time.Now(),
		Stage: stage,
		Image: img,
	}
}
