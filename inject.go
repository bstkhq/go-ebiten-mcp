package ebitenmcp

import (
	"github.com/bstkhq/go-ebiten-mcp/internal/hook"
	"github.com/hajimehoshi/ebiten/v2"
)

// Touch is one synthetic finger on the screen, in the game's logical pixels.
type Touch struct {
	// ID identifies this finger across calls, the way a real touch keeps its
	// id from the moment it lands until it lifts.
	ID int
	X  int
	Y  int
}

// Injector writes synthetic input for the game to read.
//
// It is the layer the tools and the test driver are built on, exposed for a
// game that wants to drive itself from Go — a demo mode, a replay, a soak test.
// Reach for Driver in a test and for the MCP tools from an agent; both handle
// the timing described below, and this does not.
//
// Nothing lands immediately. The wrapper applies whatever is pending inside
// Update, once per tick, before the game runs — so what is written here is what
// the game sees on its next tick.
//
// # States and events
//
// A key or a button held down, and the cursor's position, are states: they are
// rewritten every tick until something releases them, so writing one from any
// goroutine is safe and a tick's delay costs a tick.
//
// Type and Scroll are events. They reach the game for exactly one tick and are
// then gone, which is what makes them behave like the real thing — and it means
// writing one from outside the loop races with the tick it was meant for. Use
// Runtime.Do to write those where the game will see them:
//
//	rt.Do(ctx, func() { rt.Injector().Type("hello") })
//
// A build with injection compiled out, or one where the self-check could not
// confirm the mirror it writes through, leaves every method below doing
// nothing; Runtime.InputError says which. A zero Injector is a different thing
// and is not usable — take one from Runtime.Injector.
type Injector struct{ inner *hook.Injector }

// KeyDown presses a key and leaves it pressed.
func (i Injector) KeyDown(key ebiten.Key) { i.inner.KeyDown(key) }

// KeyUp releases a key held by KeyDown.
func (i Injector) KeyUp(key ebiten.Key) { i.inner.KeyUp(key) }

// MouseDown presses a mouse button and leaves it pressed.
func (i Injector) MouseDown(button ebiten.MouseButton) { i.inner.MouseDown(button) }

// MouseUp releases a mouse button.
func (i Injector) MouseUp(button ebiten.MouseButton) { i.inner.MouseUp(button) }

// MoveCursor puts the cursor at a position in the game's logical pixels, and
// pins it there until ReleaseCursor.
func (i Injector) MoveCursor(x, y float64) { i.inner.MoveCursor(x, y) }

// ReleaseCursor hands the cursor back to whatever the window reports.
func (i Injector) ReleaseCursor() { i.inner.ReleaseCursor() }

// Scroll turns the wheel. An event: see the note on Injector.
func (i Injector) Scroll(x, y float64) { i.inner.Scroll(x, y) }

// Type enters text as characters, the way a text field reads it rather than as
// key presses. An event: see the note on Injector.
func (i Injector) Type(text string) { i.inner.Type([]rune(text)) }

// SetTouches replaces the set of fingers on the screen. Passing none lifts them
// all; leaving one out lifts that one.
func (i Injector) SetTouches(touches []Touch) {
	inner := make([]hook.Touch, 0, len(touches))
	for _, t := range touches {
		inner = append(inner, hook.Touch{ID: t.ID, X: t.X, Y: t.Y})
	}
	i.inner.SetTouches(inner)
}

// ReleaseAll drops every held key, button and finger, and unpins the cursor.
// The releases are stamped properly, so the game sees a real release rather
// than input that vanished.
func (i Injector) ReleaseAll() { i.inner.ReleaseAll() }
