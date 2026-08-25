package ebitenmcp

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/hook"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// Like every test that touches Ebitengine, these need a display and the single
// game loop the process is allowed to have. TestMain owns that loop; the tests
// drive it through the Runtime, which is the thing under test.
//
// probeGame implements LayoutFer and FinalScreenDrawer so the live path through
// the optional-interface wrapper is exercised as well — by every test here,
// since the wrapper type is fixed for the life of the process. Which type gets
// built for each combination is checked separately, in
// TestWrapSelectsWrapperType, because there is only one loop to run a game in.
//
// Its final pass paints a marker that exists nowhere else. That is the whole
// point: it is what tells a capture of the final screen from a capture of the
// offscreen, and it is what would have caught this being wrong.

var (
	testRT      *Runtime
	testWrapper *wrapper
)

// markerColor is drawn only by the final pass, so finding it in a frame proves
// the frame came from after that pass.
var markerColor = color.RGBA{R: 0xff, G: 0x00, B: 0xff, A: 0xff}

const markerSize = 8

type probeGame struct {
	mu sync.Mutex

	fill     color.RGBA
	updates  int
	blockFor time.Duration
	panicIn  string
	still    bool

	// watchPad latches a gamepad button's just-pressed edge from inside Update.
	//
	// It has to be seen from in here. IsGamepadButtonJustPressed is true for
	// exactly one tick, and a test polling it from outside with Do reads only
	// the ticks its calls happen to land on — so it misses the edge whenever it
	// falls in a gap, which is a test that fails once in a while for no reason
	// anybody can act on. A game's Update runs every tick and cannot miss it.
	watchPad       *ebiten.GamepadID
	sawJustPressed bool

	// typed latches what AppendInputChars handed over, for the same reason
	// watchPad exists: runes live for exactly one tick, so a test reading them
	// from outside sees only the ticks its own calls land on.
	typed []rune

	marker *ebiten.Image
}

func newProbeGame(fill color.RGBA) *probeGame {
	return &probeGame{fill: fill}
}

func (g *probeGame) Update() error {
	g.mu.Lock()
	g.updates++
	block := g.blockFor
	g.blockFor = 0
	panicIn := g.panicIn
	g.mu.Unlock()

	if panicIn == "update" {
		panic("probeGame: deliberate panic in Update")
	}

	g.mu.Lock()
	if g.watchPad != nil && inpututil.IsGamepadButtonJustPressed(*g.watchPad, 0) {
		g.sawJustPressed = true
	}
	g.typed = append(g.typed, ebiten.AppendInputChars(nil)...)
	g.mu.Unlock()
	if block > 0 {
		time.Sleep(block)
	}
	return nil
}

func (g *probeGame) Draw(screen *ebiten.Image) {
	g.mu.Lock()
	fill, panicIn, still := g.fill, g.panicIn, g.still
	g.mu.Unlock()

	// A game that draws nothing is the shape Ebitengine stops compositing, and
	// the only way to reach that path from a test. Everything else here fills
	// the screen every frame, which is exactly why this went unnoticed.
	if still {
		return
	}

	screen.Fill(fill)

	if panicIn == "draw" {
		panic("probeGame: deliberate panic in Draw")
	}
}

func (g *probeGame) Layout(int, int) (int, int) {
	g.mu.Lock()
	panicIn := g.panicIn
	g.mu.Unlock()

	if panicIn == "layout" {
		panic("probeGame: deliberate panic in Layout")
	}
	return 64, 48
}

// LayoutF is the one Ebitengine actually calls here, since this game implements
// LayoutFer. Layout panics too, so a wrapper built for a game without LayoutFer
// would be covered as well.
func (g *probeGame) LayoutF(float64, float64) (float64, float64) {
	g.mu.Lock()
	panicIn := g.panicIn
	g.mu.Unlock()

	if panicIn == "layout" {
		panic("probeGame: deliberate panic in LayoutF")
	}
	return 64, 48
}

// DrawFinalScreen composites the offscreen and then stamps the marker, which is
// the only place in this game that colour is ever drawn.
func (g *probeGame) DrawFinalScreen(screen ebiten.FinalScreen, offscreen *ebiten.Image, geoM ebiten.GeoM) {
	ebiten.DefaultDrawFinalScreen(screen, offscreen, geoM)

	if g.marker == nil {
		g.marker = ebiten.NewImage(markerSize, markerSize)
		g.marker.Fill(markerColor)
	}
	screen.DrawImage(g.marker, nil)
}

// watchGamepad starts latching the just-pressed edge of button 0.
func (g *probeGame) watchGamepad(id ebiten.GamepadID) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.watchPad, g.sawJustPressed = &id, false
}

func (g *probeGame) sawPress() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.sawJustPressed
}

func (g *probeGame) block(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.blockFor = d
}

func TestMain(m *testing.M) {
	// With a factory, so game_reset has something to reset to. Without one it
	// can only answer that it was not configured, which is a third of what the
	// tool does and the least interesting third. Nothing else notices: reset
	// below swaps the game with SetGame and never asks for the factory.
	fill := color.RGBA{R: 0x20, G: 0x40, B: 0x80, A: 0xff}
	factory := func() ebiten.Game { return newProbeGame(fill) }

	wrapped, rt := Wrap(factory(), WithFactory(factory), WithPanicRecovery(true))
	testRT = rt

	// The live wrapper, for the one test that has to look at what it is holding.
	// probeGame implements both optional interfaces, so this is the type Wrap
	// builds; if that ever stops being true the assertion here says so at once
	// rather than a test failing for an unrelated-looking reason.
	testWrapper = wrapped.(*wrapperLayoutFFinalScreen).wrapper

	result := make(chan int, 1)
	go func() {
		code := m.Run()
		rt.Terminate()
		result <- code
	}()

	ebiten.SetWindowSize(320, 240)
	ebiten.SetVsyncEnabled(false)

	if err := ebiten.RunGame(wrapped); err != nil {
		fmt.Fprintln(os.Stderr, "harness game failed:", err)
		os.Exit(1)
	}

	os.Exit(<-result)
}

// reset puts the runtime back to a known state so tests do not inherit each
// other's pauses or crashes.
func reset(t *testing.T) *probeGame {
	t.Helper()

	game := newProbeGame(color.RGBA{R: 0x20, G: 0x40, B: 0x80, A: 0xff})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testRT.Resume()
	if err := testRT.SetGame(ctx, game); err != nil {
		t.Fatalf("resetting the game: %v", err)
	}
	return game
}

// settle waits for the loop to come to rest after a pause.
//
// Pause takes effect at a tick boundary, not at the instant it is called: the
// loop may already be past shouldUpdate and on its way to advance, so exactly
// one more tick can land afterwards. A test that took its baseline before that
// tick saw the counter move and called it a bug — once in every few runs, which
// is the worst rate there is.
func settle(t *testing.T) int64 {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	last := testRT.Tick()

	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)

		now := testRT.Tick()
		if now == last {
			return now
		}
		last = now
	}

	t.Fatalf("the loop never came to rest after being paused; it is at tick %d", last)
	return 0
}

// TestInputInjectionIsAvailable is the loud half of the mirror's defence.
//
// hook.Verify writes a pattern through the mirrored struct and reads it back
// through Ebitengine's public API on the first tick, which is the check that
// catches a layout that has drifted. But its failure was silent: the error went
// into Runtime.inputErr, the wrapper simply stopped injecting, and every test
// that needed input called t.Skipf on it — so a mirror pointing at the wrong
// offsets produced a green run with some skips in it, which nobody reads.
//
// On a build that supports injection, it not working is a failure. That is the
// whole point of the tag: ebitenmcp_nohook is how you say you do not want this.
func TestInputInjectionIsAvailable(t *testing.T) {
	if !hook.Supported {
		t.Skip("built with ebitenmcp_nohook, which is how you ask for this to be absent")
	}

	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify runs on the first tick, so wait for one before asking.
	if err := testRT.WaitTicks(ctx, 2); err != nil {
		t.Fatalf("waiting for the first tick: %v", err)
	}

	if err := testRT.InputError(); err != nil {
		t.Fatalf("input injection is not working: %v\n\n"+
			"hook.Verify could not confirm the mirror of ui.InputState. Nothing will be "+
			"injected while that is true, and the tests that need input will skip rather "+
			"than fail — which is why this one exists. Check internal/upstream first: it "+
			"hashes the declaration this mirrors.", err)
	}
}

func TestTicksAdvance(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	before := testRT.Tick()
	if err := testRT.WaitTicks(ctx, 3); err != nil {
		t.Fatalf("waiting for ticks: %v", err)
	}

	if got := testRT.Tick() - before; got < 3 {
		t.Errorf("advanced %d ticks, want at least 3", got)
	}
}

// TestPauseHoldsTheGame is the one place where wall-clock time is the right
// measure: the claim is that no ticks happen at all, and waiting for a tick
// that must never arrive can only be done by waiting.
func TestPauseHoldsTheGame(t *testing.T) {
	game := reset(t)

	testRT.Pause()
	tick := settle(t)
	updates := game.count()

	time.Sleep(200 * time.Millisecond)

	if got := testRT.Tick(); got != tick {
		t.Errorf("tick moved from %d to %d while paused", tick, got)
	}
	if got := game.count(); got != updates {
		t.Errorf("game updated %d times while paused", got-updates)
	}
}

func TestStepRunsExactlyN(t *testing.T) {
	game := reset(t)

	testRT.Pause()
	settle(t)
	start := game.count()

	testRT.Step(3)
	time.Sleep(200 * time.Millisecond)

	if got := game.count() - start; got != 3 {
		t.Errorf("stepping 3 ran %d updates", got)
	}
	if paused, steps := testRT.Paused(); !paused || steps != 0 {
		t.Errorf("after stepping: paused=%v steps=%d, want paused with no steps left", paused, steps)
	}
}

func TestCaptureReturnsTheGamesOwnResolution(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frame, err := testRT.CaptureStage(ctx, StageOffscreen)
	if err != nil {
		t.Fatalf("capturing: %v", err)
	}

	if got := frame.Image.Bounds(); got.Dx() != 64 || got.Dy() != 48 {
		t.Errorf("captured %v, want the 64x48 the game lays out", got)
	}

	// Ebitengine premultiplies and round-trips through the GPU, so compare
	// loosely rather than demanding exact bytes.
	r, g, b, _ := frame.Image.At(32, 24).RGBA()
	if abs(int(r>>8)-0x20) > 2 || abs(int(g>>8)-0x40) > 2 || abs(int(b>>8)-0x80) > 2 {
		t.Errorf("captured pixel is (%d,%d,%d), want roughly (32,64,128)", r>>8, g>>8, b>>8)
	}
}

// TestCaptureSeesTheFinalPass is the regression for the bug this file exists to
// prevent coming back.
//
// Ebitengine draws twice — the game's Draw fills an offscreen, then
// DrawFinalScreen composites it onto the real screen — and capturing at the
// first step returns an image the player never saw. Any final pass at all
// hides in that gap: a CRT filter, scanlines, a letterbox, a scaling filter.
// The marker stands in for all of them.
//
// The final screen cannot simply be read: ebiten.FinalScreen has no ReadPixels.
// So this also covers the mechanism, which is handing the game an image of our
// own to draw into and blitting it onward.
func TestCaptureSeesTheFinalPass(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	final, err := testRT.CaptureStage(ctx, StageFinal)
	if err != nil {
		t.Fatalf("capturing the final screen: %v", err)
	}
	offscreen, err := testRT.CaptureStage(ctx, StageOffscreen)
	if err != nil {
		t.Fatalf("capturing the offscreen: %v", err)
	}

	if final.Stage != StageFinal || offscreen.Stage != StageOffscreen {
		t.Errorf("frames came back labelled %q and %q", final.Stage, offscreen.Stage)
	}

	// The final screen is the window, which is a scaled-up 64x48. Asserting the
	// exact size would be asserting the size of whatever window the test
	// happened to get; that it is bigger is the part that means something.
	if final.Image.Bounds().Dx() <= offscreen.Image.Bounds().Dx() {
		t.Errorf("the final screen is %v and the offscreen %v: the final pass is not being read at the screen's own resolution",
			final.Image.Bounds(), offscreen.Image.Bounds())
	}

	// Two pixels in, to stay clear of any filtering at the very edge.
	if r, g, b, _ := final.Image.At(2, 2).RGBA(); abs(int(r>>8)-0xff) > 4 || g>>8 > 4 || abs(int(b>>8)-0xff) > 4 {
		t.Errorf("the final screen has (%d,%d,%d) where the final pass drew its marker: the capture is from before that pass",
			r>>8, g>>8, b>>8)
	}

	// And the same spot in the offscreen is the game's own fill, which is what
	// makes asking for the offscreen worth doing: it is how you tell a bug in
	// the game from a bug in the pass over it.
	if r, g, b, _ := offscreen.Image.At(0, 0).RGBA(); abs(int(r>>8)-0x20) > 2 || abs(int(g>>8)-0x40) > 2 || abs(int(b>>8)-0x80) > 2 {
		t.Errorf("the offscreen has (%d,%d,%d) at the corner, want the game's own fill: the marker leaked into it",
			r>>8, g>>8, b>>8)
	}
}

// TestCaptureSurvivesASkippedFinalPass is the regression for the worst way this
// could go wrong.
//
// Ebitengine stops calling DrawFinalScreen after four frames in which nothing
// drew into the offscreen, which a game that holds its picture still reaches as
// soon as it stops clearing the screen every frame. Captures for a game with a
// final pass are served from DrawFinalScreen, so before this was handled they
// were never served at all: the caller waited out its whole timeout and was then
// told the game loop had stopped — while it was running perfectly and reporting
// so through every other tool. Being told the opposite of what is happening is
// worse than being told nothing.
func TestCaptureSurvivesASkippedFinalPass(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Process-global, so it goes back on the way out. Every other game here
	// fills the screen each frame, so no other test can see it.
	ebiten.SetScreenClearedEveryFrame(false)
	defer func() {
		ebiten.SetScreenClearedEveryFrame(true)
		reset(t)
	}()

	still := newProbeGame(color.RGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xff})
	still.still = true

	if err := testRT.SetGame(ctx, still); err != nil {
		t.Fatalf("installing the still game: %v", err)
	}

	// Well past Ebitengine's skip threshold of four.
	if err := testRT.WaitTicks(ctx, 20); err != nil {
		t.Fatalf("waiting for the game to settle: %v", err)
	}

	frame, err := testRT.CaptureStage(ctx, StageFinal)
	if err != nil {
		t.Fatalf("capturing a game that is holding still: %v\n"+
			"the final pass was skipped and nobody served the request", err)
	}
	if frame.Image.Bounds().Empty() {
		t.Errorf("captured an empty frame: %v", frame.Image.Bounds())
	}

	// And again, to be sure it was not one lucky frame on the way into the
	// skipping state.
	if _, err := testRT.CaptureStage(ctx, StageFinal); err != nil {
		t.Fatalf("the second capture of a still game failed: %v", err)
	}
}

// TestFinalCopyIsReleasedWhenIdle covers the other half of the deal. The copy is
// the size of the window — 33 MB at 4K — and a game that was looked at once must
// not go on holding it for the rest of its life.
//
// The idling is done on the game loop rather than by waiting five seconds for
// it: those fields belong to the loop, and reaching into them from here would
// be both a race and a slow test.
func TestFinalCopyIsReleasedWhenIdle(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := testRT.CaptureStage(ctx, StageFinal); err != nil {
		t.Fatalf("capturing the final screen: %v", err)
	}

	var held, released bool
	if err := testRT.Do(ctx, func() {
		held = testWrapper.final != nil

		for i := 0; i < finalIdleFrames; i++ {
			testWrapper.releaseFinal()
		}
		released = testWrapper.final == nil
	}); err != nil {
		t.Fatalf("running on the loop: %v", err)
	}

	if !held {
		t.Error("nothing was kept for a capture that asked for the final screen")
	}
	if !released {
		t.Errorf("the copy is still held after %d idle frames", finalIdleFrames)
	}
}

// TestSetGameSwapsWhatIsRunning is what makes independent tests possible in one
// binary: Ebitengine's loop cannot be restarted, so starting over has to mean
// replacing the game inside the wrapper.
func TestSetGameSwapsWhatIsRunning(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := testRT.SetGame(ctx, newProbeGame(color.RGBA{R: 0xff, A: 0xff})); err != nil {
		t.Fatalf("swapping the game: %v", err)
	}

	frame, err := testRT.Capture(ctx)
	if err != nil {
		t.Fatalf("capturing: %v", err)
	}

	r, _, b, _ := frame.Image.At(32, 24).RGBA()
	if r>>8 < 0xf0 || b>>8 > 0x0f {
		t.Errorf("after the swap the screen is (%d,_,%d), want the new game's red", r>>8, b>>8)
	}
}

// TestSurvivesAPanicInUpdate covers the reason this wrapper recovers at all: a
// crashed game must still be inspectable. If the process died with the game,
// every question worth asking would die with it.
func TestSurvivesAPanicInUpdate(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	crasher := newProbeGame(color.RGBA{G: 0xff, A: 0xff})
	crasher.panicIn = "update"

	if err := testRT.SetGame(ctx, crasher); err != nil {
		t.Fatalf("installing the crashing game: %v", err)
	}

	crash := waitForCrash(t)
	if crash.Phase != "update" {
		t.Errorf("crash phase is %q, want %q", crash.Phase, "update")
	}
	if crash.Value == "" || crash.Stack == "" {
		t.Errorf("crash recorded without a value or a stack: %+v", crash)
	}

	// The loop is still turning, so the runtime still answers.
	if _, err := testRT.Capture(ctx); err != nil {
		t.Errorf("cannot capture after the crash: %v", err)
	}

	// And swapping in a healthy game brings it back.
	reset(t)
	if got := testRT.Crash(); got != nil {
		t.Errorf("crash survived the swap: %+v", got)
	}
}

func TestSurvivesAPanicInDraw(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	crasher := newProbeGame(color.RGBA{B: 0xff, A: 0xff})
	crasher.panicIn = "draw"

	if err := testRT.SetGame(ctx, crasher); err != nil {
		t.Fatalf("installing the crashing game: %v", err)
	}

	crash := waitForCrash(t)
	if crash.Phase != "draw" {
		t.Errorf("crash phase is %q, want %q", crash.Phase, "draw")
	}

	// A panic in Draw leaves half a frame on a screen that is about to be
	// cleared, so the wrapper grabs it there and then. It is usually the most
	// useful thing in the report.
	if testRT.LastFrame() == nil {
		t.Error("no frame was kept from the moment of the crash")
	}

	reset(t)
}

// TestDoReturnsAPanicInsteadOfDyingOfIt covers the hole in "the game panicked
// and the server is still answering".
//
// Everything a tool reads or writes runs through Do, inside the loop, and a
// panic in there used to unwind through the loop and take the process with it.
// game_inspect can cause one just by reading — reflection over a value taken
// from an unexported field panics on Interface() — so a tool marked read-only
// could kill the game it was inspecting, and the agent would be left with a
// closed socket and no idea why.
func TestDoReturnsAPanicInsteadOfDyingOfIt(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := testRT.Do(ctx, func() { panic("a tool asked for something impossible") })

	var raised *CommandPanic
	if !errors.As(err, &raised) {
		t.Fatalf("Do returned %v, want a CommandPanic — if the process got this far the panic was swallowed somewhere else", err)
	}
	if !strings.Contains(raised.Value, "impossible") {
		t.Errorf("the panic came back as %q, without what was panicked", raised.Value)
	}
	if !strings.Contains(raised.Stack, "TestDoReturnsAPanic") {
		t.Errorf("the stack does not reach the code that panicked:\n%s", raised.Stack)
	}

	// And the loop is untouched: this is not a crash of the game.
	if crash := testRT.Crash(); crash != nil {
		t.Errorf("a panic in a queued command was recorded as a crash of the game: %+v", crash)
	}
	if err := testRT.WaitTicks(ctx, 3); err != nil {
		t.Errorf("the loop stopped after a command panicked: %v", err)
	}
}

// TestLayoutPanicIsSurvivable covers the one phase that had no net. Ebitengine
// calls Layout every frame, so a game that panics there panicked sixty times a
// second and took the process with it the first time.
func TestLayoutPanicIsSurvivable(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bad := newProbeGame(color.RGBA{B: 0x40, A: 0xff})
	bad.panicIn = "layout"

	if err := testRT.SetGame(ctx, bad); err != nil {
		t.Fatalf("installing the game: %v", err)
	}

	crash := waitForCrash(t)
	if crash.Phase != "layout" {
		t.Errorf("crash phase is %q, want %q", crash.Phase, "layout")
	}

	// The fallback has to be positive or Ebitengine panics on it in turn, which
	// would defeat the whole point of catching this.
	width, height := testWrapper.layoutFallback(0, 0)
	if width <= 0 || height <= 0 {
		t.Errorf("the fallback layout is %dx%d, which Ebitengine would panic on", width, height)
	}

	reset(t)
}

// TestDoTimesOutWhenTheLoopStalls is the guarantee that a debugging tool never
// hangs along with the thing it is debugging.
func TestDoTimesOutWhenTheLoopStalls(t *testing.T) {
	game := reset(t)
	game.block(1500 * time.Millisecond)

	// Let the blocking update start.
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := testRT.Do(ctx, func() {})
	if err != ErrLoopStalled {
		t.Fatalf("Do on a stalled loop returned %v, want ErrLoopStalled", err)
	}

	// And a stalled loop is visible as such, rather than looking like a paused
	// or idle one.
	if age := time.Since(testRT.LastTick()); age < 100*time.Millisecond {
		t.Errorf("LastTick is only %v old, so a stall would look like a healthy game", age)
	}

	time.Sleep(1600 * time.Millisecond)
	reset(t)
}

func TestTimingsAreRecorded(t *testing.T) {
	reset(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := testRT.WaitTicks(ctx, 5); err != nil {
		t.Fatalf("waiting for ticks: %v", err)
	}

	timings := testRT.Timings()
	if len(timings) < 5 {
		t.Fatalf("got %d timings, want at least 5", len(timings))
	}

	last := timings[len(timings)-1]
	if last.Update <= 0 {
		t.Errorf("update time is %d ns, want something positive", last.Update)
	}
	if last.Draw <= 0 {
		t.Errorf("draw time is %d ns, want something positive", last.Draw)
	}
}

// TestWrapSelectsWrapperType covers all four combinations of the optional
// interfaces, which the live loop cannot: it only runs one game.
func TestWrapSelectsWrapperType(t *testing.T) {
	for _, tc := range []struct {
		name        string
		game        ebiten.Game
		wantLayoutF bool
		wantFinal   bool
	}{
		{"plain", &plainGame{}, false, false},
		{"layoutF", &layoutFGame{}, true, false},
		{"finalScreen", &finalScreenGame{}, false, true},
		{"both", &bothGame{}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped, _ := Wrap(tc.game)

			if _, ok := wrapped.(ebiten.LayoutFer); ok != tc.wantLayoutF {
				t.Errorf("wrapper implements LayoutFer = %v, want %v", ok, tc.wantLayoutF)
			}
			if _, ok := wrapped.(ebiten.FinalScreenDrawer); ok != tc.wantFinal {
				t.Errorf("wrapper implements FinalScreenDrawer = %v, want %v", ok, tc.wantFinal)
			}
		})
	}
}

// TestRunGameDoesNotWrapWithoutAServer is the release-build half of the
// integration contract: with EBITEN_MCP_ADDR unset, RunGame really is
// Ebitengine's RunGame. In particular there is no wrapper present to recover a
// panic into a Runtime that nobody can reach.
func TestRunGameDoesNotWrapWithoutAServer(t *testing.T) {
	t.Setenv(AddrEnv, "")

	game := &panickingGame{}
	got, rt := gameForRun(game, nil)

	if got != game {
		t.Errorf("gameForRun returned %T, want the original game", got)
	}
	if rt != nil {
		t.Error("gameForRun built a Runtime with no MCP server configured")
	}

	var value any
	func() {
		defer func() { value = recover() }()
		_ = got.Update()
	}()
	if value != panicValue {
		t.Errorf("Update panicked with %v, want %q", value, panicValue)
	}
}

func TestPanicRecoveryIsOptIn(t *testing.T) {
	t.Setenv(PanicRecoveryEnv, "")

	t.Run("off by default", func(t *testing.T) {
		wrapped, rt := Wrap(&panickingGame{}, WithAddr(""))

		var value any
		func() {
			defer func() { value = recover() }()
			_ = wrapped.(*wrapper).updateGame()
		}()

		if value != panicValue {
			t.Errorf("Update panicked with %v, want %q", value, panicValue)
		}
		if crash := rt.Crash(); crash != nil {
			t.Errorf("default recovery recorded a crash: %+v", crash)
		}
	})

	t.Run("enabled explicitly", func(t *testing.T) {
		wrapped, rt := Wrap(&panickingGame{}, WithAddr(""), WithPanicRecovery(true))

		if err := wrapped.(*wrapper).updateGame(); err != nil {
			t.Fatalf("Update returned %v after recovering the panic", err)
		}
		crash := rt.Crash()
		if crash == nil {
			t.Fatal("the opted-in recovery did not record the panic")
		}
		if crash.Phase != "update" || crash.Value != panicValue {
			t.Errorf("recorded crash is %+v, want the Update panic %q", crash, panicValue)
		}
	})

	t.Run("enabled by environment", func(t *testing.T) {
		t.Setenv(PanicRecoveryEnv, "1")
		wrapped, rt := Wrap(&panickingGame{}, WithAddr(""))

		if err := wrapped.(*wrapper).updateGame(); err != nil {
			t.Fatalf("Update returned %v after recovering the panic", err)
		}
		if crash := rt.Crash(); crash == nil || crash.Value != panicValue {
			t.Errorf("environment-enabled recovery recorded %+v, want %q", crash, panicValue)
		}
	})

	t.Run("option overrides environment", func(t *testing.T) {
		t.Setenv(PanicRecoveryEnv, "1")
		wrapped, rt := Wrap(&panickingGame{}, WithAddr(""), WithPanicRecovery(false))

		var value any
		func() {
			defer func() { value = recover() }()
			_ = wrapped.(*wrapper).updateGame()
		}()

		if value != panicValue {
			t.Errorf("Update panicked with %v, want %q", value, panicValue)
		}
		if crash := rt.Crash(); crash != nil {
			t.Errorf("disabled recovery recorded a crash: %+v", crash)
		}
	})
}

type plainGame struct{}

func (*plainGame) Update() error              { return nil }
func (*plainGame) Draw(*ebiten.Image)         {}
func (*plainGame) Layout(int, int) (int, int) { return 1, 1 }

const panicValue = "the game's panic must stay visible"

type panickingGame struct{ plainGame }

func (*panickingGame) Update() error { panic(panicValue) }

type layoutFGame struct{ plainGame }

func (*layoutFGame) LayoutF(float64, float64) (float64, float64) { return 1, 1 }

type finalScreenGame struct{ plainGame }

func (*finalScreenGame) DrawFinalScreen(ebiten.FinalScreen, *ebiten.Image, ebiten.GeoM) {}

type bothGame struct{ finalScreenGame }

func (*bothGame) LayoutF(float64, float64) (float64, float64) { return 1, 1 }

func (g *probeGame) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.updates
}

func waitForCrash(t *testing.T) *Crash {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if crash := testRT.Crash(); crash != nil {
			return crash
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("the game never recorded a crash")
	return nil
}
