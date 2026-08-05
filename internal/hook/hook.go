//go:build !ebitenmcp_nohook

// Package hook reaches into Ebitengine's internals to write synthetic input
// into the state the game reads every tick.
//
// Ebitengine has no public API for this. ebiten.IsKeyPressed and friends read
// from internal/inputstate, which the backend fills from the window's event
// queue; nothing else can write there. So this package pulls four internal
// symbols in with //go:linkname:
//
//	ui.Get()                        the singleton UserInterface
//	(*ui.UserInterface).Tick()      the tick "just pressed" is measured against
//	(*ui.UserInterface).InputTime() the clock every input event is stamped with
//	inputstate.(*inputState).Update the mutator the backend itself uses
//
// The last one hands us a *ui.InputState, whose fields are all exported but
// whose type we cannot name. inputState below mirrors its layout field by
// field. That mirror is the fragile part: a field inserted into ui.InputState
// in a future Ebitengine release would silently shift every offset after it.
// Three things guard against that, in the order they fire: TestInputStateLayout
// hashes the upstream declaration, Verify writes and reads back a pattern
// through Ebitengine's public API before the first tick, and the
// ebitenmcp_nohook build tag drops this file entirely.
//
// On the legality of all this: the //go:linkname restrictions added in Go 1.23
// only cover symbols defined in the standard library (cmd/link's checkLinkname
// bails with "For now, only check for symbols defined in std"). Pulling into a
// third-party module is still allowed, though the proposal that introduced the
// check says they would like to require the handshake form everywhere
// eventually. See the injector interface in the parent package for the way out.
//
// Verified against ebiten v2.9.9.
package hook

import (
	"io/fs"
	"unsafe"

	"github.com/hajimehoshi/ebiten/v2"

	// The linkname targets live in packages we cannot import by name, but the
	// linker still needs them linked in; importing ebiten pulls both.
	_ "unsafe"
)

//go:linkname uiGet github.com/hajimehoshi/ebiten/v2/internal/ui.Get
func uiGet() unsafe.Pointer

//go:linkname uiInputTime github.com/hajimehoshi/ebiten/v2/internal/ui.(*UserInterface).InputTime
func uiInputTime(u unsafe.Pointer) inputTime

//go:linkname uiTick github.com/hajimehoshi/ebiten/v2/internal/ui.(*UserInterface).Tick
func uiTick(u unsafe.Pointer) int64

//go:linkname inputstateGet github.com/hajimehoshi/ebiten/v2/internal/inputstate.Get
func inputstateGet() unsafe.Pointer

//go:linkname inputstateUpdate github.com/hajimehoshi/ebiten/v2/internal/inputstate.(*inputState).Update
func inputstateUpdate(s unsafe.Pointer, fn func(unsafe.Pointer))

// inputTime mirrors ui.InputTime: a tick in the high bits and a per-tick event
// counter in the low ones. Ebitengine's "just pressed" test is a comparison of
// the tick half against the current tick, which is why the value a key was
// stamped with has to be preserved across ticks rather than refreshed.
type inputTime int64

const inputTimeSubtickBits = 20

// Tick reports the tick an input time was stamped in.
func (i inputTime) Tick() int64 {
	return int64(i >> inputTimeSubtickBits)
}

// touch mirrors ui.Touch.
type touch struct {
	ID int
	X  int
	Y  int
}

// inputState mirrors ui.InputState. Fields must stay in declaration order and
// nothing may be inserted; see the package comment.
//
// The array bounds are the load-bearing part, and they come from the public
// enums while upstream's come from the internal ones. That is safe, and not by
// luck: ebiten converts between them with a plain numeric cast — input.go does
// inputstate.Get().IsKeyPressed(ui.Key(key)) — so the two enumerations have to
// agree value for value or Ebitengine's own IsKeyPressed would read the wrong
// slot. Measured against v2.9.9: KeyMax is 121 either way, MouseButtonMax 4, and
// ui.TouchID is an int, which is what makes the touch mirror below the same
// shape.
//
// Nothing here is ever allocated. update casts a pointer Ebitengine already
// owns, so this struct's total size does not matter; only the offsets of the
// fields written through it, every one of which Verify checks by reading back
// through the public API.
type inputState struct {
	KeyPressedTimes  [ebiten.KeyMax + 1]inputTime
	KeyReleasedTimes [ebiten.KeyMax + 1]inputTime

	MouseButtonPressedTimes  [ebiten.MouseButtonMax + 1]inputTime
	MouseButtonReleasedTimes [ebiten.MouseButtonMax + 1]inputTime

	CursorX           float64
	CursorY           float64
	WheelX            float64
	WheelY            float64
	Touches           []touch
	Runes             []rune
	WindowBeingClosed bool
	DroppedFiles      fs.FS
}

// Tick returns the tick Ebitengine is currently running. During a game's Update
// this is the value IsKeyJustPressed compares against: the counter is only
// incremented after Update returns (internal/ui/context.go).
func Tick() int64 {
	return uiTick(uiGet())
}

// stamp allocates an input time in the current tick, exactly as the window
// backend does when it records a real event. It must be called from the game
// loop, so that the tick it lands in is the one the game is about to read.
func stamp() inputTime {
	return uiInputTime(uiGet())
}

// update runs fn against the live input state under Ebitengine's own lock.
//
// That lock is not reentrant and inputstate takes it for every read, so fn must
// not call ebiten.IsKeyPressed or anything else that reads input: it would
// deadlock against itself. Write in here, read outside.
func update(fn func(*inputState)) {
	inputstateUpdate(inputstateGet(), func(p unsafe.Pointer) {
		fn((*inputState)(p))
	})
}
