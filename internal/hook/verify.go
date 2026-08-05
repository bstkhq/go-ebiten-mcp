//go:build !ebitenmcp_nohook

package hook

import (
	"fmt"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// Supported reports whether this build can inject input at all. It is false
// only in the ebitenmcp_nohook variant.
const Supported = true

// probe values, chosen to be things no real input would produce.
const (
	probeCursorX = 12345
	probeCursorY = 54321
	probeWheelX  = 7
	probeWheelY  = -3
	probeTouchID = 0x5eed
	probeTouchX  = 1234
	probeTouchY  = 4321
	probeRune    = 'ⓥ'
)

// probeKey is the highest-numbered physical key. The four keys above it
// (KeyAlt, KeyControl, KeyShift, KeyMeta) are virtual and never read from their
// own slot, so this is as far up the array as the public API can see. Probing
// near the top means a change in the array's length shows up here.
const probeKey = ebiten.KeyTab

// Verify writes a pattern through the mirrored struct and reads it back with
// Ebitengine's public API, to prove the mirror still lines up with the real
// ui.InputState before anything depends on it.
//
// It must be called from the game loop, before the game's Update, and it leaves
// the input state exactly as it found it: the game never observes the probes.
//
// The checks run in increasing field offset order and stop at the first
// failure. That ordering is deliberate. If a field were inserted upstream, the
// earliest check fails and the later, further-out writes never happen, which
// keeps a layout drift from reaching past the end of the struct.
//
// Note that reads happen between calls to update, never inside one.
// inputstate's mutator holds its (non-reentrant) mutex while it runs the
// callback, so calling ebiten.IsKeyPressed from in there deadlocks against
// itself. Being inside the game's own Update is what makes the split safe:
// nothing else writes to the input state at that point.
func Verify() error {
	if err := verifyKeys(); err != nil {
		return err
	}
	if err := verifyMouse(); err != nil {
		return err
	}
	if err := verifyCursor(); err != nil {
		return err
	}
	if err := verifyWheel(); err != nil {
		return err
	}
	if err := verifyTouches(); err != nil {
		return err
	}
	return verifyRunes()
}

// verifyKeys covers both key arrays. The released array sits right behind the
// pressed one, so checking a release as well is what detects a change in the
// number of keys: the second array would have moved.
//
// The read-back goes through inpututil rather than ebiten because that is the
// package games actually use for edge detection, and confirming it sees the
// injected state is the whole point.
func verifyKeys() error {
	var oldPressed, oldReleased inputTime

	// Saved and put back, rather than zeroed. Verify runs on the first tick, and
	// Tab is a key somebody may genuinely be holding right then — alt-tabbing
	// into the window is how you would arrive. Zeroing it would swallow that
	// press, which is a strange thing for a self-check to do and contradicts
	// what this function promises.
	defer update(func(s *inputState) {
		s.KeyPressedTimes[probeKey] = oldPressed
		s.KeyReleasedTimes[probeKey] = oldReleased
	})

	update(func(s *inputState) {
		oldPressed, oldReleased = s.KeyPressedTimes[probeKey], s.KeyReleasedTimes[probeKey]
		s.KeyPressedTimes[probeKey] = stamp()
	})
	pressed := ebiten.IsKeyPressed(probeKey)

	update(func(s *inputState) { s.KeyReleasedTimes[probeKey] = stamp() })
	justReleased := inpututil.IsKeyJustReleased(probeKey)

	if !pressed {
		return fmt.Errorf("KeyPressedTimes does not drive ebiten.IsKeyPressed")
	}
	if !justReleased {
		return fmt.Errorf("KeyReleasedTimes does not drive inpututil.IsKeyJustReleased")
	}
	return nil
}

func verifyMouse() error {
	const button = ebiten.MouseButtonMax

	var oldPressed, oldReleased inputTime

	defer update(func(s *inputState) {
		s.MouseButtonPressedTimes[button] = oldPressed
		s.MouseButtonReleasedTimes[button] = oldReleased
	})

	update(func(s *inputState) {
		oldPressed, oldReleased = s.MouseButtonPressedTimes[button], s.MouseButtonReleasedTimes[button]
		s.MouseButtonPressedTimes[button] = stamp()
	})
	pressed := ebiten.IsMouseButtonPressed(button)

	update(func(s *inputState) { s.MouseButtonReleasedTimes[button] = stamp() })
	justReleased := inpututil.IsMouseButtonJustReleased(button)

	if !pressed {
		return fmt.Errorf("MouseButtonPressedTimes does not drive ebiten.IsMouseButtonPressed")
	}
	if !justReleased {
		return fmt.Errorf("MouseButtonReleasedTimes does not drive inpututil.IsMouseButtonJustReleased")
	}
	return nil
}

func verifyCursor() error {
	var oldX, oldY float64

	update(func(s *inputState) {
		oldX, oldY = s.CursorX, s.CursorY
		s.CursorX, s.CursorY = probeCursorX, probeCursorY
	})
	x, y := ebiten.CursorPosition()
	update(func(s *inputState) { s.CursorX, s.CursorY = oldX, oldY })

	if x != probeCursorX || y != probeCursorY {
		return fmt.Errorf("CursorX/Y read back as (%d,%d), want (%d,%d)",
			x, y, probeCursorX, probeCursorY)
	}
	return nil
}

func verifyWheel() error {
	var oldX, oldY float64

	update(func(s *inputState) {
		oldX, oldY = s.WheelX, s.WheelY
		s.WheelX, s.WheelY = probeWheelX, probeWheelY
	})
	x, y := ebiten.Wheel()
	update(func(s *inputState) { s.WheelX, s.WheelY = oldX, oldY })

	if x != probeWheelX || y != probeWheelY {
		return fmt.Errorf("WheelX/Y read back as (%v,%v), want (%v,%v)",
			x, y, probeWheelX, probeWheelY)
	}
	return nil
}

func verifyTouches() error {
	var old []touch

	update(func(s *inputState) {
		old = s.Touches
		s.Touches = []touch{{ID: probeTouchID, X: probeTouchX, Y: probeTouchY}}
	})
	ids := ebiten.AppendTouchIDs(nil)
	x, y := ebiten.TouchPosition(probeTouchID)
	update(func(s *inputState) { s.Touches = old })

	if len(ids) != 1 || ids[0] != probeTouchID {
		return fmt.Errorf("Touches read back as %v, want one touch with id %d", ids, probeTouchID)
	}
	if x != probeTouchX || y != probeTouchY {
		return fmt.Errorf("touch position read back as (%d,%d), want (%d,%d)",
			x, y, probeTouchX, probeTouchY)
	}
	return nil
}

func verifyRunes() error {
	var old []rune

	update(func(s *inputState) {
		old = s.Runes
		s.Runes = []rune{probeRune}
	})
	runes := ebiten.AppendInputChars(nil)
	update(func(s *inputState) { s.Runes = old })

	if len(runes) != 1 || runes[0] != probeRune {
		return fmt.Errorf("Runes read back as %q, want %q", string(runes), string(probeRune))
	}
	return nil
}
