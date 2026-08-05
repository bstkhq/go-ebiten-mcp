package ebitenmcp

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
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
	if block > 0 {
		time.Sleep(block)
	}
	return nil
}

func (g *probeGame) Draw(screen *ebiten.Image) {
	g.mu.Lock()
	fill, panicIn := g.fill, g.panicIn
	g.mu.Unlock()

	screen.Fill(fill)

	if panicIn == "draw" {
		panic("probeGame: deliberate panic in Draw")
	}
}

func (g *probeGame) Layout(int, int) (int, int) { return 64, 48 }

func (g *probeGame) LayoutF(float64, float64) (float64, float64) { return 64, 48 }

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

func (g *probeGame) block(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.blockFor = d
}

func TestMain(m *testing.M) {
	wrapped, rt := Wrap(newProbeGame(color.RGBA{R: 0x20, G: 0x40, B: 0x80, A: 0xff}))
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
	tick, updates := testRT.Tick(), game.count()

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
	time.Sleep(50 * time.Millisecond)
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

type plainGame struct{}

func (*plainGame) Update() error              { return nil }
func (*plainGame) Draw(*ebiten.Image)         {}
func (*plainGame) Layout(int, int) (int, int) { return 1, 1 }

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
