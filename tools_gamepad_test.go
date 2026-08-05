package ebitenmcp

import (
	"strings"
	"testing"

	"github.com/bstkhq/go-ebiten-mcp/internal/uinput"
	"github.com/hajimehoshi/ebiten/v2"
)

// game_gamepad was the last whole tool with no test of its own: the gamepad
// tests below it drive the Go API, and the tool's own layer — the axis
// arithmetic, the which-controller rule, the connect-and-use-it-in-one-call
// shortcut — had never run outside a live session.

// TestScaleAxisKnowsATriggerFromAStick is the arithmetic that makes "all the
// way" mean something. A stick runs -32767..32767 and a trigger 0..255, so a
// caller cannot be asking for a number; it has to be a proportion, and the two
// shapes do not scale the same way.
func TestScaleAxisKnowsATriggerFromAStick(t *testing.T) {
	// The layout rather than the identity: the arithmetic works on the ranges
	// each axis declares, and an identity carries none, since no caller picks
	// them.
	profile := DefaultGamepadIdentity().layout()

	for _, c := range []struct {
		name  string
		code  uint16
		value float64
		want  int32
	}{
		{"a stick pushed right", uinput.AbsX, 1, 32767},
		{"a stick pushed left", uinput.AbsX, -1, -32767},
		{"a stick at rest", uinput.AbsX, 0, 0},
		{"a stick halfway", uinput.AbsX, 0.5, 16383},

		// A trigger has no negative half. Both zero and minus one are a finger
		// off the trigger, which is what a caller sending a stick value to one
		// by mistake means, whichever they sent.
		{"a trigger pulled", uinput.AbsZ, 1, 255},
		{"a trigger released", uinput.AbsZ, 0, 0},
		{"a trigger pushed the wrong way", uinput.AbsZ, -1, 0},
		{"a trigger halfway", uinput.AbsZ, 0.5, 127},

		// Out of range is a caller's slip, not a reason to send the device a
		// number it never declared.
		{"a stick past the end", uinput.AbsX, 4, 32767},
		{"a stick past the other end", uinput.AbsX, -4, -32767},
	} {
		if got := scaleAxis(profile, c.code, c.value); got != c.want {
			t.Errorf("%s: %g became %d, want %d", c.name, c.value, got, c.want)
		}
	}
}

// TestScaleAxisPassesThroughAnAxisTheProfileNeverDeclared: there is no range to
// scale into, so the value goes as it came rather than being scaled by a range
// belonging to some other axis.
func TestScaleAxisPassesThroughAnAxisTheProfileNeverDeclared(t *testing.T) {
	if got := scaleAxis(DefaultGamepadIdentity().layout(), 0xbeef, 1); got != 1 {
		t.Errorf("an undeclared axis scaled to %d, want the value itself", got)
	}
}

// TestGamepadIDSaysWhichWayItIsAmbiguous. The tool takes id optionally, because
// one controller is the normal case and naming it every time would be noise.
// Both ways that can fail have to say what to do about it: nothing plugged in
// is a different fix from several plugged in.
func TestGamepadIDSaysWhichWayItIsAmbiguous(t *testing.T) {
	s := newTestServer(t)
	pads := testRT.Gamepads()

	if _, err := s.gamepadID(pads, intPtr(7)); err != nil {
		t.Errorf("an explicit id was refused: %v", err)
	}

	// Nothing plugged in. This one needs no device, which is why it is here and
	// not with the tests below.
	if len(pads.Connected()) == 0 {
		_, err := s.gamepadID(pads, nil)
		if err == nil {
			t.Fatal("no controller and no id, and it picked one anyway")
		}
		if !strings.Contains(err.Error(), "connect") {
			t.Errorf("the error does not say how to fix it: %v", err)
		}
	}
}

// TestGamepadToolPlugsInPressesAndUnplugs is the tool's whole life in one call
// each, which is how an agent uses it: connect, and from then on say what the
// controller is doing without naming it.
func TestGamepadToolPlugsInPressesAndUnplugs(t *testing.T) {
	requireGamepads(t)
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, out, err := s.gamepad(ctx, nil, gamepadInput{Connect: &gamepadProfileInput{}})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if out.Connected == nil {
		t.Fatal("nothing came back to say which controller was plugged in")
	}
	id := ebiten.GamepadID(*out.Connected)
	t.Cleanup(func() { testRT.Gamepads().Disconnect(id) })

	if out.SDLID == "" {
		t.Error("no SDL id, so a game cannot tell what kind of controller this is")
	}

	// Buttons, a stick and the d-pad in one call, since that is what makes it
	// one movement rather than three: the tool sends them together.
	if _, _, err = s.gamepad(ctx, nil, gamepadInput{
		Buttons: map[string]bool{"a": true},
		Axes:    map[string]float64{"leftx": 1},
		Dpad:    "up",
	}); err != nil {
		t.Fatalf("pressing: %v", err)
	}

	if err := testRT.WaitTicks(ctx, 3); err != nil {
		t.Fatalf("waiting: %v", err)
	}

	var pressed, up bool
	var leftX float64
	if err := testRT.Do(ctx, func() {
		pressed = ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightBottom)
		up = ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftTop)
		leftX = ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisLeftStickHorizontal)
	}); err != nil {
		t.Fatalf("reading: %v", err)
	}

	if !pressed {
		t.Error(`the game does not see "a" pressed`)
	}
	if !up {
		t.Error("the game does not see the d-pad pushed up")
	}
	if leftX < 0.9 {
		t.Errorf("the left stick reads %.2f, want it pushed right", leftX)
	}

	// Unplugging has to say so and stop claiming the controller is there.
	_, out, err = s.gamepad(ctx, nil, gamepadInput{Disconnect: true})
	if err != nil {
		t.Fatalf("disconnecting: %v", err)
	}
	if out.Disconnected == nil || *out.Disconnected != int(id) {
		t.Errorf("disconnect answered %v, want %d", out.Disconnected, id)
	}
	if out.GamepadID != nil {
		t.Error("it still reports a controller after unplugging it")
	}
}

// TestGamepadToolRefusesToGuessWithNothingPluggedIn: the id is optional because
// one controller is the normal case, and the failure has to name the fix rather
// than press a button on a controller that is not there.
func TestGamepadToolRefusesToGuessWithNothingPluggedIn(t *testing.T) {
	requireGamepads(t)
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if pads := testRT.Gamepads(); len(pads.Connected()) > 0 {
		t.Skip("a controller from another test is still plugged in")
	}

	_, _, err := s.gamepad(ctx, nil, gamepadInput{Buttons: map[string]bool{"a": true}})
	if err == nil {
		t.Fatal("it pressed a button on a controller that is not plugged in")
	}
	if !strings.Contains(err.Error(), "connect") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}
