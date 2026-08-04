package ebitenmcp

import (
	"fmt"
	"image/color"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// wrapper is the ebiten.Game handed to Ebitengine in place of the real one.
//
// Update is the only place where anything is allowed to touch game state, so
// the order inside it is the whole contract: run queued work, apply synthetic
// input, then give the game its tick. Input has to land after the queue,
// because a command may have just asked for a key to be held, and before the
// game, because that is the tick it must be visible in.
type wrapper struct {
	rt *Runtime

	lastStart  time.Time
	drawTime   time.Duration
	crashImage *ebiten.Image
}

func (w *wrapper) Update() error {
	start := time.Now()

	wall := time.Duration(0)
	if !w.lastStart.IsZero() {
		wall = start.Sub(w.lastStart)
	}
	w.lastStart = start

	w.rt.verifyInput()
	w.rt.drainCommands()

	// Injection writes through a struct that mirrors an Ebitengine internal. If
	// the self-check could not confirm that mirror, writing through it could
	// corrupt memory, so it is better to do nothing at all.
	if w.rt.InputError() == nil && !w.rt.injector.Idle() {
		w.rt.injector.Apply()
	}

	if w.rt.terminated() {
		return ebiten.Termination
	}

	if !w.rt.shouldUpdate() {
		return nil
	}

	err := w.updateGame()
	w.rt.advance(time.Since(start), w.drawTime, wall)
	return err
}

// updateGame calls the game and turns a panic into recorded state instead of a
// dead process.
//
// Surviving the game's own crash is the point: the frames, the traces and the
// stack are all still there to be asked about, which is the difference between
// this and finding a stack trace in a terminal after the fact.
func (w *wrapper) updateGame() (err error) {
	defer func() {
		if v := recover(); v != nil {
			w.rt.recordCrash("update", v)
			err = nil
		}
	}()

	return w.rt.currentGame().Update()
}

func (w *wrapper) Draw(screen *ebiten.Image) {
	start := time.Now()

	if crash := w.rt.Crash(); crash != nil {
		w.drawCrash(screen, crash)
	} else {
		w.crashImage = nil
		w.drawGame(screen)
	}

	w.drawTime = time.Since(start)
	w.rt.recordDraw(w.drawTime)
	w.rt.serveCaptures(screen)
}

func (w *wrapper) drawGame(screen *ebiten.Image) {
	defer func() {
		if v := recover(); v != nil {
			w.rt.recordCrash("draw", v)

			// Grab whatever the game managed to draw before it died. The
			// screen is cleared at the start of every frame, so this half-drawn
			// image only exists right here, and it is often the most useful
			// thing in the whole report.
			w.rt.keepFrame(screen)
		}
	}()

	w.rt.currentGame().Draw(screen)
}

// drawCrash keeps the window showing something truthful after the game has
// died, rather than an empty screen that looks like a rendering bug.
//
// The last frame is uploaded once and kept: a crashed game may sit there being
// inspected for minutes, and rebuilding the same texture sixty times a second
// for all of them would be silly.
func (w *wrapper) drawCrash(screen *ebiten.Image, crash *Crash) {
	if w.crashImage == nil {
		if last := w.rt.LastFrame(); last != nil {
			w.crashImage = ebiten.NewImageFromImage(last.Image)
		}
	}
	if w.crashImage != nil {
		screen.DrawImage(w.crashImage, nil)
	}

	// The last frame is still the game's own artwork, so the message needs
	// something solid behind it. Printed straight on top it lands on whatever
	// the game was drawing and neither can be read.
	banner := screen.Bounds()
	banner.Max.Y = banner.Min.Y + 46
	vector.DrawFilledRect(screen,
		float32(banner.Min.X), float32(banner.Min.Y),
		float32(banner.Dx()), float32(banner.Dy()),
		color.RGBA{R: 0x50, G: 0x0c, B: 0x0c, A: 0xf2}, false)

	ebitenutil.DebugPrintAt(screen, fmt.Sprintf(
		"PANIC in %s at tick %d\n%s\nthe game is stopped; the MCP server is still answering",
		crash.Phase, crash.Tick, crash.Value), 8, 6)
}

func (w *wrapper) Layout(outsideWidth, outsideHeight int) (int, int) {
	return w.rt.currentGame().Layout(outsideWidth, outsideHeight)
}

// Ebitengine picks up two optional interfaces by type assertion, and Go cannot
// implement an interface conditionally. So there is one wrapper type per
// combination and Wrap returns whichever matches the game it was given.
//
// Each forwards defensively rather than assuming the game it started with:
// SetGame can swap in a game that implements a different set, and a wrapper
// that assumed otherwise would call a method that is not there.

type wrapperLayoutF struct{ *wrapper }

func (w *wrapperLayoutF) LayoutF(outsideWidth, outsideHeight float64) (float64, float64) {
	if g, ok := w.rt.currentGame().(ebiten.LayoutFer); ok {
		return g.LayoutF(outsideWidth, outsideHeight)
	}

	sw, sh := w.rt.currentGame().Layout(int(outsideWidth), int(outsideHeight))
	return float64(sw), float64(sh)
}

type wrapperFinalScreen struct{ *wrapper }

func (w *wrapperFinalScreen) DrawFinalScreen(screen ebiten.FinalScreen, offscreen *ebiten.Image, geoM ebiten.GeoM) {
	drawFinalScreen(w.rt, screen, offscreen, geoM)
}

type wrapperLayoutFFinalScreen struct{ *wrapperLayoutF }

func (w *wrapperLayoutFFinalScreen) DrawFinalScreen(screen ebiten.FinalScreen, offscreen *ebiten.Image, geoM ebiten.GeoM) {
	drawFinalScreen(w.rt, screen, offscreen, geoM)
}

// drawFinalScreen forwards to the game, or reproduces Ebitengine's own default
// when the current game does not want to draw the final screen itself.
func drawFinalScreen(rt *Runtime, screen ebiten.FinalScreen, offscreen *ebiten.Image, geoM ebiten.GeoM) {
	if g, ok := rt.currentGame().(ebiten.FinalScreenDrawer); ok {
		g.DrawFinalScreen(screen, offscreen, geoM)
		return
	}

	op := &ebiten.DrawImageOptions{Filter: ebiten.FilterLinear}
	op.GeoM = geoM
	screen.DrawImage(offscreen, op)
}

// wrap builds the wrapper type matching the optional interfaces the game
// implements.
func wrap(rt *Runtime) ebiten.Game {
	base := &wrapper{rt: rt}

	_, layoutF := rt.currentGame().(ebiten.LayoutFer)
	_, finalScreen := rt.currentGame().(ebiten.FinalScreenDrawer)

	switch {
	case layoutF && finalScreen:
		return &wrapperLayoutFFinalScreen{&wrapperLayoutF{base}}
	case layoutF:
		return &wrapperLayoutF{base}
	case finalScreen:
		return &wrapperFinalScreen{base}
	default:
		return base
	}
}

func sprint(v any) string {
	return fmt.Sprint(v)
}
