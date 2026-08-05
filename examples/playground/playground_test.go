package main

import (
	"testing"

	ebitenmcp "github.com/bstkhq/go-ebiten-mcp"
	"github.com/hajimehoshi/ebiten/v2"
)

// These tests are also the worked example of the test driver. They need a
// display like everything else that touches Ebitengine, so run them through the
// harness with `make test` rather than a bare `go test`.
//
// One TestMain, one game loop, and a fresh game per test. That is not a style
// choice: ebiten.RunGame cannot be called twice in a process, so isolation has
// to come from replacing the game rather than restarting the loop.

func TestMain(m *testing.M) {
	ebitenmcp.RunTests(m, func() ebiten.Game { return NewGame() })
}

// TestMenuMovesOneRowPerPress is the test that would catch the subtlest thing
// the injection could get wrong. If a held key reported itself as newly pressed
// on every tick, the selection would run away down the list.
func TestMenuMovesOneRowPerPress(t *testing.T) {
	d := ebitenmcp.T(t)

	for want := int64(1); want <= 3; want++ {
		d.Tap(ebiten.KeyArrowDown)

		if got := d.Inspect("screens.menu.selected"); got != want {
			t.Fatalf("after %d presses the selection is %v, want %d", want, got, want)
		}
	}

	d.Tap(ebiten.KeyEnter)
	if got := d.Inspect("screens.menu.chosen"); got != "credits" {
		t.Errorf("enter chose %v, want credits", got)
	}
}

// TestHoldingAKeyAccelerates covers the other half: a key that stays down has
// to keep reading as pressed, with a duration that grows.
func TestHoldingAKeyAccelerates(t *testing.T) {
	d := ebitenmcp.T(t)
	d.Tap(ebiten.KeyDigit2)

	before := d.Inspect("screens.player.pos.x").(float64)

	// The duration has to be read while the key is still down: Ebitengine
	// reports zero the moment it is released, so checking after a Hold would
	// always find nothing and prove nothing.
	d.KeyDown(ebiten.KeyArrowRight)
	d.Tick(30)

	held := d.Inspect("screens.player.held")
	after := d.Inspect("screens.player.pos.x").(float64)

	d.KeyUp(ebiten.KeyArrowRight)
	d.Tick(1)

	if after <= before+20 {
		t.Errorf("the player moved from %.1f to %.1f, which is not what holding a key for 30 ticks should do",
			before, after)
	}
	if held.(int64) < 25 {
		t.Errorf("the game saw a press duration of %v ticks after holding for 30; it was read as a tap, not a hold", held)
	}
}

// TestDragLeavesAStroke checks a gesture by what it left behind, which is the
// only way an agent can check one after the fact.
func TestDragLeavesAStroke(t *testing.T) {
	d := ebitenmcp.T(t)
	d.Tap(ebiten.KeyDigit3)

	d.Drag(80, 90, 400, 240, 20)

	strokes, ok := d.Inspect("screens.paint.strokes").([]any)
	if !ok || len(strokes) != 1 {
		t.Fatalf("after one drag the game has %v strokes, want exactly one", d.Inspect("screens.paint.strokes"))
	}

	points, ok := strokes[0].(map[string]any)["points"].([]any)
	if !ok || len(points) < 10 {
		t.Errorf("the stroke has %d points; a drag should be a path, not a jump", len(points))
	}
}

func TestTypedTextReachesTheField(t *testing.T) {
	d := ebitenmcp.T(t)
	d.Tap(ebiten.KeyDigit5)

	d.Type("hola")
	if got := d.Inspect("screens.form.text"); got != "hola" {
		t.Fatalf("the field holds %q, want %q", got, "hola")
	}

	d.Tap(ebiten.KeyEnter)
	d.Type("mundo")

	if got := d.Inspect("screens.form.text"); got != "mundo" {
		t.Errorf("after enter the field holds %q, want %q", got, "mundo")
	}
}

// TestWithGameRunsInsideTheLoop covers the escape hatch that used to be a trap.
//
// Driver.Game handed the live object back, and the caller then read it from the
// test's goroutine while Update was running — which is the one thing everything
// else in this package goes through Do to prevent. WithGame runs the closure
// where the game is not.
func TestWithGameRunsInsideTheLoop(t *testing.T) {
	d := ebitenmcp.T(t)

	d.Tap(ebiten.KeyArrowDown)

	var selected int
	d.WithGame(func(game ebiten.Game) {
		// Read straight off the real type, which is the point of this over
		// Inspect — and safe only because of where it runs.
		selected = game.(*Game).screens.menu.selected
	})

	if selected != 1 {
		t.Errorf("read selected=%d after one press, want 1", selected)
	}
}

// TestMenuLooksRight is the visual regression: run with -update to record what
// the menu should look like, and from then on any unintended change to it fails
// here with the expected, actual and difference images written to testdata.
func TestMenuLooksRight(t *testing.T) {
	d := ebitenmcp.T(t)

	d.Tap(ebiten.KeyArrowDown)
	d.Golden("menu_second_row.png")
}
