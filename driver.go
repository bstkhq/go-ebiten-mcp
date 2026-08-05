package ebitenmcp

import (
	"context"
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/hook"
	"github.com/hajimehoshi/ebiten/v2"
)

// The test driver is the same machinery as the MCP server with the protocol
// taken off: step the loop, inject input, capture frames. It exists because a
// server is no use for writing a test, and because testing was half the point.
//
// Its shape is dictated by one hard fact about Ebitengine: RunGame can only be
// called once per process. It creates an OS thread, a render thread and a GLFW
// window, and the flag it sets on the way out is never cleared, so everything
// afterwards behaves as though the game had ended. There is therefore no
// RunGame per test. TestMain owns the single loop and every test runs against
// it, starting from a fresh game rather than a fresh loop.

var session *Runtime

// RunTests runs a package's tests against one game loop.
//
// Put it in TestMain and take the driver in each test:
//
//	func TestMain(m *testing.M) {
//	    ebitenmcp.RunTests(m, func() ebiten.Game { return NewGame() })
//	}
//
//	func TestMenu(t *testing.T) {
//	    d := ebitenmcp.T(t)
//	    d.Tap(ebiten.KeyArrowDown)
//	    d.Golden("menu_selected.png")
//	}
//
// It does not return: like os.Exit in a plain TestMain, it ends the process
// with the suite's status.
func RunTests(m *testing.M, factory func() ebiten.Game, opts ...Option) {
	if factory == nil {
		fmt.Fprintln(os.Stderr, "ebitenmcp: RunTests needs a factory to build a game for each test")
		os.Exit(1)
	}

	registerGoldenFlag()

	wrapped, rt := Wrap(factory(), append(opts, WithFactory(factory))...)
	session = rt

	// Tests do not want to wait for a monitor. Off vsync, the loop runs as fast
	// as the machine allows and a hundred-tick test takes milliseconds.
	ebiten.SetVsyncEnabled(false)

	code := make(chan int, 1)
	go func() {
		result := m.Run()
		rt.Terminate()
		code <- result
	}()

	err := ebiten.RunGame(wrapped)

	// Before either exit, and before anything else is printed. Close puts the
	// descriptors back, so what follows goes straight to the terminal instead of
	// through a goroutine that os.Exit will not wait for — which is how the
	// failure message and the last of the test output used to be lost.
	rt.Close()

	if err != nil {
		fmt.Fprintln(os.Stderr, "ebitenmcp: the game loop failed:", err)
		os.Exit(1)
	}

	os.Exit(<-code)
}

// Driver drives the game from a test.
//
// Every method fails the test rather than returning an error. A test that has
// lost the game loop has nothing useful left to do, and threading errors
// through every call would bury the test's own intent.
type Driver struct {
	t  *testing.T
	rt *Runtime

	// Timeout bounds each operation. It is generous by default because a
	// software renderer in CI is slow, not because anything should take long.
	Timeout time.Duration

	// GoldenTolerance is the per-channel difference two pixels may have and
	// still count as equal.
	GoldenTolerance int

	// GoldenMaxDiff is the fraction of pixels allowed to differ beyond the
	// tolerance before a golden comparison fails.
	GoldenMaxDiff float64
}

// T returns a driver for one test, with a game freshly built by the factory.
//
// Resetting rather than restarting is what makes tests independent inside the
// one loop the process is allowed.
func T(t *testing.T) *Driver {
	t.Helper()

	if session == nil {
		t.Fatal("ebitenmcp: no session; call ebitenmcp.RunTests from TestMain")
	}

	d := &Driver{
		t:               t,
		rt:              session,
		Timeout:         30 * time.Second,
		GoldenTolerance: 2,
		GoldenMaxDiff:   0.001,
	}

	d.rt.Resume()
	d.rt.Injector().ReleaseAll()

	ctx, cancel := context.WithTimeout(context.Background(), d.Timeout)
	defer cancel()

	if err := d.rt.Reset(ctx); err != nil {
		t.Fatalf("ebitenmcp: could not start a fresh game: %v", err)
	}

	// Two ticks so the reset has been through Update and Draw once, which is
	// what a game expects before anything asks it questions.
	d.Tick(2)

	// A build that ought to be able to inject and cannot is every test's
	// problem, so it is reported here, before the test does any work, rather
	// than at the first key press. A build with injection compiled out is not
	// the same thing and is not a failure - see injector.
	if hook.Supported {
		if err := d.rt.InputError(); err != nil {
			t.Fatalf("ebitenmcp: input injection is unavailable: %v", err)
		}
	}

	t.Cleanup(func() {
		d.rt.Injector().ReleaseAll()
		d.rt.Resume()
	})

	return d
}

// injector is the driver's way in to input, and so the one place that has to
// notice this build cannot do any.
//
// Compiled out with -tags ebitenmcp_nohook, a test that presses a key is not
// failing: there is nothing there to fail, and nothing about the game it could
// be reporting. It is skipped, naming the tag, since that is the one fact that
// would let somebody put it back. Tests that only draw and read still run,
// which is most of what the tag is for - a game that had to drop injection to
// build against a new Ebitengine keeps its golden images.
func (d *Driver) injector() *hook.Injector {
	d.t.Helper()

	d.requireInjection()
	return d.rt.Injector()
}

// requireInjection is the check on its own, for the two methods that do their
// writing inside the loop: skipping a test is the test goroutine's business and
// must not happen on the game's.
func (d *Driver) requireInjection() {
	d.t.Helper()

	if !hook.Supported {
		d.t.Skip("ebitenmcp: this build injects no input (-tags ebitenmcp_nohook)")
	}
}

func (d *Driver) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d.Timeout)
}

func (d *Driver) fatal(err error) {
	d.t.Helper()

	if err == ErrLoopStalled {
		crash := d.rt.Crash()
		if crash != nil {
			d.t.Fatalf("the game panicked in %s at tick %d: %s\n\n%s",
				crash.Phase, crash.Tick, crash.Value, crash.Stack)
		}
		d.t.Fatalf("the game loop stopped running: last tick %d was %s ago",
			d.rt.Tick(), time.Since(d.rt.LastTick()).Round(time.Millisecond))
	}
	d.t.Fatal(err)
}

// Tick lets the game run n ticks.
func (d *Driver) Tick(n int) {
	d.t.Helper()

	ctx, cancel := d.ctx()
	defer cancel()

	if err := d.rt.WaitTicks(ctx, n); err != nil {
		d.fatal(err)
	}
}

// Tap presses keys for one tick and releases them, which is what a quick key
// press looks like to a game.
func (d *Driver) Tap(keys ...ebiten.Key) {
	d.t.Helper()
	d.Hold(1, keys...)
}

// Hold presses keys, runs the given number of ticks, then releases them.
func (d *Driver) Hold(ticks int, keys ...ebiten.Key) {
	d.t.Helper()

	if ticks < 1 {
		ticks = 1
	}

	for _, k := range keys {
		d.injector().KeyDown(k)
	}
	d.Tick(ticks)

	for _, k := range keys {
		d.injector().KeyUp(k)
	}
	d.Tick(1)
}

// KeyDown presses keys and leaves them pressed.
func (d *Driver) KeyDown(keys ...ebiten.Key) {
	for _, k := range keys {
		d.injector().KeyDown(k)
	}
}

// KeyUp releases keys held by KeyDown.
func (d *Driver) KeyUp(keys ...ebiten.Key) {
	for _, k := range keys {
		d.injector().KeyUp(k)
	}
}

// Type enters text as characters, the way a text field reads it.
func (d *Driver) Type(text string) {
	d.t.Helper()

	d.requireInjection()

	ctx, cancel := d.ctx()
	defer cancel()

	// From inside the loop: runes are an event, and writing one from here is a
	// race with the very frame this then waits for. See Runtime.injectEvent.
	if err := d.rt.injectEvent(ctx, func(inj *hook.Injector) { inj.Type([]rune(text)) }); err != nil {
		d.fatal(err)
	}
	d.Tick(1)
}

// Move puts the cursor at a position in the game's own screen pixels.
func (d *Driver) Move(x, y float64) {
	d.t.Helper()

	d.injector().MoveCursor(x, y)
	d.Tick(1)
}

// Click presses and releases at a position.
func (d *Driver) Click(x, y float64, button ...ebiten.MouseButton) {
	d.t.Helper()

	b := ebiten.MouseButtonLeft
	if len(button) > 0 {
		b = button[0]
	}

	d.Move(x, y)
	d.injector().MouseDown(b)
	d.Tick(1)
	d.injector().MouseUp(b)
	d.Tick(1)
}

// Drag walks from one point to another with the button held, over several
// ticks. A game that samples the cursor per tick sees a path, not a jump.
func (d *Driver) Drag(x0, y0, x1, y1 float64, steps int) {
	d.t.Helper()

	d.Move(x0, y0)
	d.injector().MouseDown(ebiten.MouseButtonLeft)
	d.Tick(1)

	// The same line game_mouse walks, from the same function: a test and a tool
	// that disagreed about where a drag ends would be a difference nobody could
	// see until one of them failed.
	for _, at := range dragPath(x0, y0, x1, y1, steps) {
		d.injector().MoveCursor(at[0], at[1])
		d.Tick(1)
	}

	d.injector().MouseUp(ebiten.MouseButtonLeft)
	d.Tick(1)
}

// Scroll turns the wheel.
func (d *Driver) Scroll(x, y float64) {
	d.t.Helper()

	d.requireInjection()

	ctx, cancel := d.ctx()
	defer cancel()

	// The wheel is an event too, and lasts exactly as long as a rune does.
	if err := d.rt.injectEvent(ctx, func(inj *hook.Injector) { inj.Scroll(x, y) }); err != nil {
		d.fatal(err)
	}
	d.Tick(1)
}

// Screenshot captures the next frame at the game's own resolution.
func (d *Driver) Screenshot() *image.RGBA {
	d.t.Helper()

	return d.ScreenshotStage(StageOffscreen)
}

// ScreenshotStage captures a particular stage.
//
// Screenshot reads the offscreen rather than what the player sees, and for a
// test that is the right way round: the final screen is the size of the window,
// so an assertion about it — a golden above all — would depend on the monitor
// the test happened to run on. Ask for StageFinal when the game's own
// DrawFinalScreen is the thing under test, and expect to size the window
// yourself.
func (d *Driver) ScreenshotStage(stage Stage) *image.RGBA {
	d.t.Helper()

	ctx, cancel := d.ctx()
	defer cancel()

	frame, err := d.rt.CaptureStage(ctx, stage)
	if err != nil {
		d.fatal(err)
	}
	return frame.Image
}

// Inspect reads a path out of the live game, including unexported fields.
func (d *Driver) Inspect(path string) any {
	d.t.Helper()

	ctx, cancel := d.ctx()
	defer cancel()

	var (
		value any
		err   error
	)
	if doErr := d.rt.Do(ctx, func() {
		value, err = Inspect{}.At(d.rt.currentGame(), path)
	}); doErr != nil {
		d.fatal(doErr)
	}
	if err != nil {
		d.t.Fatalf("inspecting %q: %v", path, err)
	}
	return value
}

// WithGame runs fn against the game under test, inside the game loop.
//
// Assertions are often easier to write against the real type than against an
// inspected tree, and this is how to do that safely. It used to be a Game()
// that handed the object back, which read as convenient and was a trap: the
// caller then touched a live game from the test's goroutine while Update was
// running, which is the one thing this package's whole design exists to
// prevent — every tool goes through Do for exactly this reason, and the escape
// hatch quietly did not.
//
// fn must not block: the loop is waiting for it.
func (d *Driver) WithGame(fn func(ebiten.Game)) {
	d.t.Helper()

	ctx, cancel := d.ctx()
	defer cancel()

	if err := d.rt.Do(ctx, func() { fn(d.rt.currentGame()) }); err != nil {
		d.fatal(err)
	}
}

// Runtime exposes the underlying runtime, for anything the driver does not
// wrap.
func (d *Driver) Runtime() *Runtime {
	return d.rt
}
