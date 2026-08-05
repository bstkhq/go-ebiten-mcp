package ebitenmcp

import (
	"context"
	"testing"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/uinput"
	"github.com/hajimehoshi/ebiten/v2"
)

// These need write access to /dev/uinput and read access to /dev/input/event*,
// which are root-only by default. They skip rather than fail without them: a
// machine that cannot make virtual devices is a normal machine.

func requireGamepads(t *testing.T) {
	t.Helper()

	if err := GamepadsAvailable(); err != nil {
		t.Skipf("no virtual gamepads here: %v", err)
	}
}

func connect(t *testing.T, identity GamepadIdentity) (*Gamepads, ebiten.GamepadID) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pads := testRT.Gamepads()

	id, err := pads.Connect(ctx, identity)
	if err != nil {
		t.Fatalf("connecting a gamepad: %v", err)
	}
	t.Cleanup(func() { pads.Disconnect(id) })

	return pads, id
}

// TestGamepadIsSeenByTheGame is the whole point: a device that exists only
// because we made it has to be indistinguishable from one somebody plugged in.
func TestGamepadIsSeenByTheGame(t *testing.T) {
	requireGamepads(t)
	reset(t)

	identity := DefaultGamepadIdentity()
	identity.Name = "ebitenmcp default pad"

	_, id := connect(t, identity)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		name     string
		sdlID    string
		standard bool
	)
	if err := testRT.Do(ctx, func() {
		name, sdlID = ebiten.GamepadName(id), ebiten.GamepadSDLID(id)
		standard = ebiten.IsStandardGamepadLayoutAvailable(id)
	}); err != nil {
		t.Fatalf("reading the gamepad: %v", err)
	}

	// Not identity.Name. Ebitengine's Gamepad.Name prefers the controller
	// database's name whenever the SDL id has an entry there, so an identity
	// that claims to be a known controller cannot also choose what it is called
	// — the database wins, and it calls this one "Xbox 360 Controller". An
	// identity nothing knows keeps its own name, which the custom-identity test
	// below covers.
	if name == "" {
		t.Error("the game reports no name at all")
	}
	t.Logf("the game calls it %q; the identity asked for %q", name, identity.Name)
	if sdlID != identity.SDLID() {
		t.Errorf("the game sees id %s, want %s", sdlID, identity.SDLID())
	}
	// This is what makes the default profile worth having: an id the controller
	// database knows gets the standard layout for free.
	if !standard {
		t.Error("no standard layout for the default profile, so the standard buttons will not work")
	}
}

// TestGamepadButtonsReachTheGame covers both APIs a game might use: the raw
// button indices and the standard layout, plus inpututil's edges, which is what
// most games actually call.
func TestGamepadButtonsReachTheGame(t *testing.T) {
	requireGamepads(t)
	reset(t)

	game := reset(t)
	pads, id := connect(t, DefaultGamepadIdentity())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := pads.Button(id, uinput.BtnA, true); err != nil {
		t.Fatalf("pressing A: %v", err)
	}

	var raw, standard bool
	if err := testRT.WaitTicks(ctx, 2); err != nil {
		t.Fatalf("waiting: %v", err)
	}
	if err := testRT.Do(ctx, func() {
		raw = ebiten.IsGamepadButtonPressed(id, 0)
		standard = ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightBottom)
	}); err != nil {
		t.Fatalf("reading: %v", err)
	}

	if !raw {
		t.Error("button 0 does not read as pressed")
	}
	if !standard {
		t.Error("the standard bottom-right button does not read as pressed")
	}

	if err := pads.Button(id, uinput.BtnA, false); err != nil {
		t.Fatalf("releasing A: %v", err)
	}
	if err := testRT.WaitTicks(ctx, 2); err != nil {
		t.Fatalf("waiting: %v", err)
	}

	// Press again to catch the edge, which is what a menu listens for.
	//
	// Latched from inside the game rather than polled from here: the edge is
	// true for one tick, and a poll that reads only the ticks it lands on misses
	// it whenever it falls in a gap. That failed about one run in ten and blamed
	// the gamepad for it.
	game.watchGamepad(id)

	if err := pads.Button(id, uinput.BtnA, true); err != nil {
		t.Fatalf("pressing A again: %v", err)
	}
	if err := testRT.WaitTicks(ctx, 30); err != nil {
		t.Fatalf("waiting for the press: %v", err)
	}

	if !game.sawPress() {
		t.Error("inpututil never reported the press as new, so menus would not react")
	}
}

func TestGamepadAxesReachTheGame(t *testing.T) {
	requireGamepads(t)
	reset(t)

	pads, id := connect(t, DefaultGamepadIdentity())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := pads.Axis(id, uinput.AbsX, -32767); err != nil {
		t.Fatalf("moving the stick: %v", err)
	}
	if err := testRT.WaitTicks(ctx, 3); err != nil {
		t.Fatalf("waiting: %v", err)
	}

	var value float64
	if err := testRT.Do(ctx, func() {
		value = ebiten.GamepadAxisValue(id, 0)
	}); err != nil {
		t.Fatalf("reading the axis: %v", err)
	}

	if value > -0.9 {
		t.Errorf("the stick reads %.3f, want it pushed to about -1", value)
	}
}

// TestGamepadIdentityIsConfigurable is the case that makes this useful on a
// real project: go-videogame picks a driver by parsing the vendor out of the
// SDL id, so a controller that could only claim to be an Xbox pad would send
// that game down a different path than its own hardware.
func TestGamepadIdentityIsConfigurable(t *testing.T) {
	requireGamepads(t)
	reset(t)

	identity := GamepadIdentity{
		Name:    "Advanced Gamepad",
		Vendor:  0x2a,
		Product: 0x01,
		Version: 0x01,
	}

	_, id := connect(t, identity)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var sdlID, name string
	if err := testRT.Do(ctx, func() {
		sdlID, name = ebiten.GamepadSDLID(id), ebiten.GamepadName(id)
	}); err != nil {
		t.Fatalf("reading the gamepad: %v", err)
	}

	if name != "Advanced Gamepad" {
		t.Errorf("the game calls it %q", name)
	}
	// Bytes 8 to 12, little endian, are where a game reads the vendor from.
	if got := sdlID[8:12]; got != "2a00" {
		t.Errorf("the vendor encodes as %q in %s, want 2a00", got, sdlID)
	}
}

func TestDisconnectRemovesIt(t *testing.T) {
	requireGamepads(t)
	reset(t)

	pads, id := connect(t, DefaultGamepadIdentity())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := pads.Disconnect(id); err != nil {
		t.Fatalf("disconnecting: %v", err)
	}

	gone := false
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) && !gone {
		testRT.Do(ctx, func() {
			gone = true
			for _, visible := range ebiten.AppendGamepadIDs(nil) {
				if visible == id {
					gone = false
				}
			}
		})
		if gone {
			break
		}
		testRT.WaitTicks(ctx, 1)
	}

	if !gone {
		t.Error("the game still sees the gamepad after it was unplugged")
	}
}
