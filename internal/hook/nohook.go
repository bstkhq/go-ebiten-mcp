//go:build ebitenmcp_nohook

// This variant drops the //go:linkname declarations and the mirrored struct
// entirely. Build with -tags ebitenmcp_nohook and everything else still works:
// frames, state, traces and loop control are unaffected, only input injection
// goes away. It exists so that an Ebitengine release this package has not been
// checked against can never stop a game from building.
package hook

import (
	"errors"

	"github.com/hajimehoshi/ebiten/v2"
)

// Supported reports whether this build can inject input at all.
const Supported = false

var errUnsupported = errors.New("input injection is compiled out (-tags ebitenmcp_nohook)")

// Touch is one synthetic finger on the screen, in the game's logical pixels.
type Touch struct {
	ID int
	X  int
	Y  int
}

// Injector accepts and discards everything.
type Injector struct{}

// NewInjector returns an injector that does nothing.
func NewInjector() *Injector { return &Injector{} }

func (*Injector) KeyDown(ebiten.Key)           {}
func (*Injector) KeyUp(ebiten.Key)             {}
func (*Injector) MouseDown(ebiten.MouseButton) {}
func (*Injector) MouseUp(ebiten.MouseButton)   {}
func (*Injector) MoveCursor(x, y float64)      {}
func (*Injector) ReleaseCursor()               {}
func (*Injector) Scroll(x, y float64)          {}
func (*Injector) SetTouches([]Touch)           {}
func (*Injector) Type([]rune)                  {}
func (*Injector) ReleaseAll()                  {}
func (*Injector) Idle() bool                   { return true }
func (*Injector) Apply()                       {}

// Tick reports -1, since reading Ebitengine's tick counter also needs the
// linkname this build drops.
func Tick() int64 { return -1 }

// Verify always fails, so callers report input as unavailable with a reason
// instead of silently doing nothing.
func Verify() error { return errUnsupported }
