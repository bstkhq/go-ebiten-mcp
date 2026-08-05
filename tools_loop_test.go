package ebitenmcp

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// The three loop tools nothing called: wait, set_tps and reset.
//
// game_pause, game_resume and game_step had tests from the start because they
// are what a person reaches for first. These three are what a *script* reaches
// for, which is why they went unnoticed and why they matter: waiting for a
// condition is the difference between a test written through these tools being
// deterministic and being a sequence of sleeps.
//
// probeGame.updates goes up once a tick, which makes it the state to watch.
// game_inspect already reads unexported fields, so a path of "updates" is a
// path like any other.

// TestWaitToolReturnsWhenTheValueChanges is the whole point of the tool: an
// agent that has to poll cannot tell a game that is slow from one that is
// stuck, and every poll it does costs a round trip.
func TestWaitToolReturnsWhenTheValueChanges(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, out, err := s.wait(ctx, nil, waitInput{Path: "updates", Changed: true, Timeout: 120})
	if err != nil {
		t.Fatalf("game_wait: %v", err)
	}

	if out.Matched != "changed" {
		t.Errorf("it returned on %q, want changed", out.Matched)
	}
	if out.Value == nil {
		t.Error("it did not say what the value became")
	}
	// Both halves, because "it changed" without the old value is a claim the
	// caller cannot check.
	if out.Was == nil {
		t.Error("it did not say what the value was")
	}
}

// TestWaitToolReturnsWhenTheValueArrives waits for a state the game enters and
// stays in, which is what the tool is for and what it can promise.
//
// Not a counter passing through a number: each turn of the poll costs a round
// trip into the loop and then a tick, so it samples every other tick and a
// value that is only true for one of them is a coin toss. See the note at the
// bottom of this file.
func TestWaitToolReturnsWhenTheValueArrives(t *testing.T) {
	game := reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// Flipped from another goroutine part way through, so the tool has
	// something to wait for rather than something already true.
	go func() {
		time.Sleep(100 * time.Millisecond)

		game.mu.Lock()
		game.still = true
		game.mu.Unlock()
	}()

	want := "true"

	_, out, err := s.wait(ctx, nil, waitInput{Path: "still", Equals: &want, Timeout: 300})
	if err != nil {
		t.Fatalf("game_wait: %v", err)
	}

	if out.Matched != "equals" {
		t.Errorf("it returned on %q, want equals", out.Matched)
	}
	if got := fmt.Sprint(out.Value); got != want {
		t.Errorf("it stopped at %s, want %s", got, want)
	}

	reset(t)
}

// TestWaitToolAnswersAtOnceWhenItIsAlreadyTrue.
//
// The loop reads before it waits, and that ordering is the whole reason the
// tool is usable in a sequence: an agent that has just done something and asks
// whether it took effect must not be made to wait a tick to be told yes. The
// same ordering is what puts one more read than there are ticks in the budget,
// so a value arriving on the last one still counts.
func TestWaitToolAnswersAtOnceWhenItIsAlreadyTrue(t *testing.T) {
	game := reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	game.mu.Lock()
	game.still = true
	game.mu.Unlock()

	want := "true"

	// A budget far larger than the answer needs, so that returning quickly means
	// it looked before waiting rather than that it had no room to wait.
	before := testRT.Tick()
	if _, _, err := s.wait(ctx, nil, waitInput{Path: "still", Equals: &want, Timeout: 120}); err != nil {
		t.Fatalf("a value that was already true timed out: %v", err)
	}

	// Two ticks, exactly, and measured rather than guessed: one read to record
	// what the value was, for changed, and one inside the loop to test it, each
	// costing a round trip into the loop. Moving the wait ahead of the read
	// makes it three, every time — which is what this number is here to catch,
	// so the slack that would make it comfortable would also make it useless.
	if waited := testRT.Tick() - before; waited > 2 {
		t.Errorf("it took %d ticks to report something that was true when it was asked", waited)
	}

	reset(t)
}

func TestWaitToolSaysWhatItWasWaitingForWhenItGivesUp(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// A counter that only goes up will never be this, and a small budget keeps
	// the failure cheap: the timeout path burns every tick it was given.
	never := "-1"

	_, _, err := s.wait(ctx, nil, waitInput{Path: "updates", Equals: &never, Timeout: 3})
	if err == nil {
		t.Fatal("it claimed a value arrived that never can")
	}
	for _, want := range []string{"updates", never} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so it does not say what it was waiting for: %v", want, err)
		}
	}
}

func TestWaitToolNeedsSomethingToWaitFor(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// A path and no condition is a call that would otherwise wait the full
	// default budget and then report a timeout, which reads like the game's
	// fault rather than the caller's.
	_, _, err := s.wait(ctx, nil, waitInput{Path: "updates"})
	if err == nil {
		t.Fatal("a path with no condition was accepted")
	}
	if !strings.Contains(err.Error(), "equals") || !strings.Contains(err.Error(), "changed") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// TestWaitToolCountsTicksWhenGivenNoPath is the other half of the tool, and the
// simpler one: no condition, just time measured in the game's own units rather
// than the caller's.
func TestWaitToolCountsTicksWhenGivenNoPath(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	before := testRT.Tick()

	_, out, err := s.wait(ctx, nil, waitInput{Ticks: 5})
	if err != nil {
		t.Fatalf("game_wait: %v", err)
	}

	if out.Tick-before < 5 {
		t.Errorf("it waited from tick %d to %d, which is not the five it was asked for", before, out.Tick)
	}
	if out.Waited == "" {
		t.Error("it did not say how long that took in wall clock, which is how a stalling game shows up")
	}
}

// TestSetTPSToolChangesThePaceAndCanGiveItBack.
//
// The one tool here that writes process-global Ebitengine state, on the single
// loop this whole package shares. Restoring it is not tidiness: leaving the
// game at 5 ticks a second would make every test after this one time out, in a
// way that looks like the loop had wedged.
func TestSetTPSToolChangesThePaceAndCanGiveItBack(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	before := ebiten.TPS()
	t.Cleanup(func() { ebiten.SetTPS(before) })

	_, out, err := s.setTPS(ctx, nil, setTPSInput{TPS: 30})
	if err != nil {
		t.Fatalf("game_set_tps: %v", err)
	}
	if out.TPS != 30 {
		t.Errorf("it reports %d ticks a second, want 30", out.TPS)
	}
	if ebiten.TPS() != 30 {
		t.Errorf("the engine is at %d, so the tool answered about something it did not do", ebiten.TPS())
	}

	// Zero is not "stop": it is the documented way to ask for as many ticks as
	// the display will take, which is a different number from any it could name.
	if _, _, err = s.setTPS(ctx, nil, setTPSInput{TPS: 0}); err != nil {
		t.Fatalf("game_set_tps 0: %v", err)
	}
	if ebiten.TPS() != ebiten.SyncWithFPS {
		t.Errorf("zero left the engine at %d, want it synced with the display", ebiten.TPS())
	}
}

// TestResetToolBuildsAFreshGame. Reset is what makes one test independent of
// the last inside a loop that can only be started once, so a reset that quietly
// did nothing would leave every test after the first reading the one before's
// state.
func TestResetToolBuildsAFreshGame(t *testing.T) {
	game := reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// Let the counter get somewhere first, so back near nothing means something.
	if err := testRT.WaitTicks(ctx, 30); err != nil {
		t.Fatalf("waiting: %v", err)
	}
	if game.count() < 30 {
		t.Fatalf("the game only updated %d times in thirty ticks", game.count())
	}

	_, out, err := s.reset(ctx, nil, emptyInput{})
	if err != nil {
		t.Fatalf("game_reset: %v", err)
	}
	if !out.Reset {
		t.Error("it answered without saying it had reset anything")
	}

	stale := game.count()

	// What is running now has barely started. Read straight away: given long
	// enough the fresh game reaches the same number, and then this would pass
	// whether anything had been replaced or not.
	_, fresh, err := s.inspect(ctx, nil, inspectInput{Path: "updates"})
	if err != nil {
		t.Fatalf("game_inspect: %v", err)
	}
	// Through its text, since the numeric type a path comes back as is the
	// inspector's business and not this test's.
	got, err := strconv.Atoi(fmt.Sprint(fresh.Value))
	if err != nil {
		t.Fatalf("updates came back as %#v, which is not a number", fresh.Value)
	}
	if got >= stale {
		t.Errorf("the game after the reset is at %d updates and the one before it was at %d", got, stale)
	}

	// And the old one is detached: nothing updates it any more.
	if err := testRT.WaitTicks(ctx, 5); err != nil {
		t.Fatalf("waiting: %v", err)
	}
	if game.count() != stale {
		t.Errorf("the game replaced by the reset is still being updated: %d then %d", stale, game.count())
	}

	reset(t)
}

// TestResetToolSaysSoWhenThereIsNothingToResetTo.
//
// Reset needs a factory, and a game wrapped without one is a perfectly ordinary
// way to use this library — the tool has to name that rather than fail with
// something the caller reads as a bug. It needs no loop: the check happens
// before anything is queued.
func TestResetToolSaysSoWhenThereIsNothingToResetTo(t *testing.T) {
	rt := newRuntime(newProbeGame(color.RGBA{A: 0xff}))

	s, err := newServer(rt, &Options{Name: "factoryless"}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	ctx, cancel := testContext(t)
	defer cancel()

	_, _, err = s.reset(ctx, nil, emptyInput{})
	if err == nil {
		t.Fatal("it claimed to reset a game it has no way to rebuild")
	}
	if !strings.Contains(err.Error(), "factory") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// A note on what game_wait can and cannot promise, since the tests above are
// written around it.
//
// The poll is outside the loop: each turn reads the path through Do — a round
// trip that costs about a tick on its own — and then waits another. So it
// samples roughly every other tick, and a value that is only true for one of
// them is a coin toss. Measured here, not deduced: two reads a tick apart came
// back 1 and 2, and the next pair after a single WaitTicks came back 4 and 5.
//
// For what the tool is actually for — waiting for a game to enter a state and
// stay in it — that is invisible, and the tests use exactly that. For a counter
// passing through a particular number it is not, and the answer is a timeout
// blaming a game that did reach it. Closing that would mean evaluating the
// condition inside Update rather than polling from outside, the way probeGame
// has to latch a gamepad's just-pressed edge; it is a change to the tool, not
// to these tests.
