//go:build !ebitenmcp_nohook

package hook

import (
	"fmt"
	"os"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// These tests need a real display: Ebitengine opens a GLFW window while
// internal/ui initialises and panics without one. Run them through the harness
// (`make test`), not with a bare `go test`.
//
// They also need to run inside the game loop, at the exact point a wrapped
// game's Update would sit, because that is the only place where injecting and
// reading back land in the same tick. Since ebiten.RunGame can only be called
// once in a process, TestMain owns the single loop and hands each test one tick
// at a time. It is the same shape the public RunTests helper has.

var (
	ticks  = make(chan func())
	ticked = make(chan struct{})
)

type loopGame struct{}

func (loopGame) Update() error {
	fn, ok := <-ticks
	if !ok {
		return ebiten.Termination
	}

	fn()
	ticked <- struct{}{}
	return nil
}

func (loopGame) Draw(*ebiten.Image)         {}
func (loopGame) Layout(int, int) (int, int) { return 320, 240 }

// inTick runs fn inside exactly one game tick and returns when that tick is
// over. The loop advances only when a test asks it to, so "the next tick" means
// the next call and nothing else can slip in between.
func inTick(fn func()) {
	ticks <- fn
	<-ticked
}

func TestMain(m *testing.M) {
	result := make(chan int, 1)

	go func() {
		result <- m.Run()
		close(ticks)
	}()

	ebiten.SetWindowSize(320, 240)
	ebiten.SetVsyncEnabled(false)

	if err := ebiten.RunGame(loopGame{}); err != nil {
		fmt.Fprintln(os.Stderr, "harness game failed:", err)
		os.Exit(1)
	}

	os.Exit(<-result)
}

// TestVerify is the self-check the wrapper runs before trusting the mirror. If
// this fails, every other test in the file is meaningless.
func TestVerify(t *testing.T) {
	var err error
	inTick(func() { err = Verify() })

	if err != nil {
		t.Fatalf("input state layout self-check failed: %v", err)
	}
}

type keyState struct {
	pressed      bool
	justPressed  bool
	justReleased bool
	duration     int
}

func readKey(k ebiten.Key) keyState {
	return keyState{
		pressed:      ebiten.IsKeyPressed(k),
		justPressed:  inpututil.IsKeyJustPressed(k),
		justReleased: inpututil.IsKeyJustReleased(k),
		duration:     inpututil.KeyPressDuration(k),
	}
}

// TestKeyPressLifecycle pins down the part that is easy to get wrong: a held
// key must look "just pressed" for exactly one tick, stay pressed for as long
// as it is held with a duration that grows, and produce one release edge.
//
// Re-stamping the press on every tick would make justPressed true throughout,
// which reads as a key being hammered rather than held.
func TestKeyPressLifecycle(t *testing.T) {
	inj := NewInjector()
	const key = ebiten.KeySpace

	inj.KeyDown(key)

	var held1, held2, release, after keyState
	inTick(func() { inj.Apply(); held1 = readKey(key) })
	inTick(func() { inj.Apply(); held2 = readKey(key) })

	inj.KeyUp(key)
	inTick(func() { inj.Apply(); release = readKey(key) })
	inTick(func() { inj.Apply(); after = readKey(key) })

	if want := (keyState{pressed: true, justPressed: true, duration: 1}); held1 != want {
		t.Errorf("first tick of the press: got %+v, want %+v", held1, want)
	}
	if want := (keyState{pressed: true, duration: 2}); held2 != want {
		t.Errorf("second tick of the press: got %+v, want %+v", held2, want)
	}
	if want := (keyState{justReleased: true}); release != want {
		t.Errorf("release tick: got %+v, want %+v", release, want)
	}
	if want := (keyState{}); after != want {
		t.Errorf("tick after the release: got %+v, want %+v", after, want)
	}
}

// TestKeyTapWithinOneTick covers a press and release that both arrive between
// two ticks, which is what a "tap this key" request looks like. Both edges have
// to survive into the same tick; dropping either makes taps silently do
// nothing.
func TestKeyTapWithinOneTick(t *testing.T) {
	inj := NewInjector()
	const key = ebiten.KeyEnter

	inj.KeyDown(key)
	inj.KeyUp(key)

	var tap keyState
	inTick(func() { inj.Apply(); tap = readKey(key) })

	if !tap.justPressed || !tap.justReleased {
		t.Errorf("tap: got %+v, want both edges in one tick", tap)
	}
	if !tap.pressed {
		t.Errorf("tap: key should read as pressed on the tick it was tapped, got %+v", tap)
	}
}

// TestVirtualModifiers guards a trap in Ebitengine's key table: KeyShift and
// friends have array slots that are never read, because IsKeyPressed answers
// them by looking at the left and right variants instead. Writing the slot
// would look like it worked and do nothing at all.
func TestVirtualModifiers(t *testing.T) {
	for _, tc := range []struct{ virtual, physical ebiten.Key }{
		{ebiten.KeyShift, ebiten.KeyShiftLeft},
		{ebiten.KeyControl, ebiten.KeyControlLeft},
		{ebiten.KeyAlt, ebiten.KeyAltLeft},
		{ebiten.KeyMeta, ebiten.KeyMetaLeft},
	} {
		t.Run(tc.virtual.String(), func(t *testing.T) {
			inj := NewInjector()
			inj.KeyDown(tc.virtual)

			var virtual, physical bool
			inTick(func() {
				inj.Apply()
				virtual = ebiten.IsKeyPressed(tc.virtual)
				physical = ebiten.IsKeyPressed(tc.physical)
			})
			inj.ReleaseAll()
			inTick(func() { inj.Apply() })
			inTick(func() { inj.Apply() })

			if !virtual || !physical {
				t.Errorf("pressing %v: virtual=%v physical(%v)=%v, want both true",
					tc.virtual, virtual, tc.physical, physical)
			}
		})
	}
}

func TestMouseButtonLifecycle(t *testing.T) {
	inj := NewInjector()
	const button = ebiten.MouseButtonLeft

	inj.MouseDown(button)

	var down, up bool
	var justUp bool
	inTick(func() { inj.Apply(); down = ebiten.IsMouseButtonPressed(button) })

	inj.MouseUp(button)
	inTick(func() {
		inj.Apply()
		up = ebiten.IsMouseButtonPressed(button)
		justUp = inpututil.IsMouseButtonJustReleased(button)
	})

	if !down {
		t.Error("button did not read as pressed while held")
	}
	if up {
		t.Error("button still reads as pressed after release")
	}
	if !justUp {
		t.Error("release edge was not visible")
	}
}

// TestCursorHoldsAcrossTicks matters because the window backend rewrites the
// cursor from the real pointer on every single frame. A one-shot write would be
// erased before the game ever looked at it.
func TestCursorHoldsAcrossTicks(t *testing.T) {
	inj := NewInjector()
	inj.MoveCursor(probeCursorX, probeCursorY)

	var first, second [2]int
	inTick(func() { inj.Apply(); first[0], first[1] = ebiten.CursorPosition() })
	inTick(func() { inj.Apply(); second[0], second[1] = ebiten.CursorPosition() })

	want := [2]int{probeCursorX, probeCursorY}
	if first != want || second != want {
		t.Errorf("cursor: tick1=%v tick2=%v, want %v on both", first, second, want)
	}

	inj.ReleaseCursor()

	var released [2]int
	inTick(func() { inj.Apply(); released[0], released[1] = ebiten.CursorPosition() })

	if released == want {
		t.Errorf("cursor still pinned at %v after ReleaseCursor", released)
	}
}

// TestWheelIsOneTickOnly and TestRunesAreOneTickOnly cover the inputs that are
// events rather than states. Leaving them set would make one scroll look like
// continuous scrolling forever.
func TestWheelIsOneTickOnly(t *testing.T) {
	inj := NewInjector()
	inj.Scroll(probeWheelX, probeWheelY)

	var during, after [2]float64
	inTick(func() { inj.Apply(); during[0], during[1] = ebiten.Wheel() })
	inTick(func() { inj.Apply(); after[0], after[1] = ebiten.Wheel() })

	if want := [2]float64{probeWheelX, probeWheelY}; during != want {
		t.Errorf("wheel during the scroll: got %v, want %v", during, want)
	}
	if after != ([2]float64{}) {
		t.Errorf("wheel after the scroll: got %v, want zero", after)
	}
}

func TestRunesAreOneTickOnly(t *testing.T) {
	inj := NewInjector()
	inj.Type([]rune("hola"))

	var during, after []rune
	inTick(func() { inj.Apply(); during = ebiten.AppendInputChars(nil) })
	inTick(func() { inj.Apply(); after = ebiten.AppendInputChars(nil) })

	if string(during) != "hola" {
		t.Errorf("typed runes: got %q, want %q", string(during), "hola")
	}
	if len(after) != 0 {
		t.Errorf("runes leaked into the next tick: %q", string(after))
	}
}

// TestTouches covers several fingers at once, which is the case a single
// synthetic touch would not catch.
func TestTouches(t *testing.T) {
	inj := NewInjector()
	inj.SetTouches([]Touch{
		{ID: 10, X: 1, Y: 2},
		{ID: 11, X: 3, Y: 4},
		{ID: 12, X: 5, Y: 6},
	})

	var ids []ebiten.TouchID
	positions := map[ebiten.TouchID][2]int{}
	inTick(func() {
		inj.Apply()
		ids = ebiten.AppendTouchIDs(nil)
		for _, id := range ids {
			x, y := ebiten.TouchPosition(id)
			positions[id] = [2]int{x, y}
		}
	})

	if len(ids) != 3 {
		t.Fatalf("touch ids: got %v, want three", ids)
	}
	for id, want := range map[ebiten.TouchID][2]int{10: {1, 2}, 11: {3, 4}, 12: {5, 6}} {
		if got := positions[id]; got != want {
			t.Errorf("touch %d at %v, want %v", id, got, want)
		}
	}

	inj.SetTouches(nil)

	var remaining []ebiten.TouchID
	inTick(func() { inj.Apply(); remaining = ebiten.AppendTouchIDs(nil) })

	if len(remaining) != 0 {
		t.Errorf("touches survived being cleared: %v", remaining)
	}
}

// TestIdleAfterRelease makes sure finished presses are retired. If they were
// not, the injector would keep rewriting them every tick forever and a game
// would never see the key go away.
func TestIdleAfterRelease(t *testing.T) {
	inj := NewInjector()

	if !inj.Idle() {
		t.Fatal("a fresh injector should be idle")
	}

	inj.KeyDown(ebiten.KeyA)
	if inj.Idle() {
		t.Fatal("injector is idle while holding a key")
	}

	inTick(func() { inj.Apply() })
	inj.KeyUp(ebiten.KeyA)
	inTick(func() { inj.Apply() })
	inTick(func() { inj.Apply() })

	if !inj.Idle() {
		t.Error("injector never went back to idle after the key was released")
	}
}
