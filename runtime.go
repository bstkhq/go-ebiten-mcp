// Package ebitenmcp turns a running Ebitengine game into something an agent can
// see and drive: frames, state, traces and synthetic input, over MCP.
//
// The whole integration is one line, replacing ebiten.RunGame with this
// package's. Nothing happens until EBITEN_MCP_ADDR is set, so the call can stay
// in a shipped build without opening a port or starting a goroutine. Setting it
// is what opens one, and there is no authentication behind it — see the README.
package ebitenmcp

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/hook"
	"github.com/hajimehoshi/ebiten/v2"
)

// ErrLoopStalled is returned by anything that needs the game loop when the loop
// is not running it. A crashed game, a paused one that nobody resumed, or an
// Update stuck in a deadlock all look like this.
//
// It is a distinct error because "the game is wedged" is a diagnosis, not a
// failure of the tool that reported it.
var ErrLoopStalled = errors.New("ebitenmcp: the game loop did not run the request in time")

// Crash records a panic the wrapped game raised.
type Crash struct {
	Value string    `json:"value"`
	Stack string    `json:"stack"`
	Tick  int64     `json:"tick"`
	When  time.Time `json:"when"`
	Phase string    `json:"phase"` // "update", "draw" or "final"
}

// Runtime is the state behind a wrapped game: the tick counter, the command
// queue, the input injector and the captures.
//
// Everything that reads or writes game state has to go through Do, which runs
// it inside Update. Everything that describes the runtime itself is readable
// from any goroutine at any time, on purpose: those are exactly the questions
// worth asking when the loop has stopped answering.
type Runtime struct {
	// mu guards the loop's own state and nothing else. What used to be under it
	// — the timing ring, the capture queue, the published snapshots — has moved
	// to types of its own, because a mutex held by a dozen unrelated things
	// stops telling anybody what it protects. See timings.go, capture.go and
	// inspect.go.
	mu sync.Mutex

	// What the loop is running, and what to build a fresh one from.
	game    ebiten.Game
	factory func() ebiten.Game

	// Injection, and whether the mirror it writes through was confirmed.
	injector *hook.Injector
	inputErr error
	verified bool

	// The clock. tickCh is closed and replaced on every tick, which is how
	// WaitTicks is woken.
	started  time.Time
	tick     int64
	lastTick time.Time
	tickCh   chan struct{}

	// Whether the game gets this tick.
	paused      bool
	steps       int
	terminating bool
	crash       *Crash

	// Work queued from other goroutines to run inside Update. Two queues,
	// because there are two moments in a tick worth running at and they are on
	// opposite sides of the one thing that makes them different: commands are
	// drained before the injector is applied, reads after it. See Do and
	// doAfterInput.
	commands chan func()
	reads    chan func()

	// Whether the wrapper was built for LayoutFer. Fixed before the loop starts
	// and checked by SetGame, since Ebitengine asserts on the wrapper's concrete
	// type and that cannot change.
	//
	// Its FinalScreenDrawer counterpart lives on captures, which is the only
	// thing that has to decide anything with it; this reads it from there rather
	// than keeping a third copy of one fact.
	hasLayoutF bool

	// Lazily created, so a game nobody looks at pays for none of them.
	server   *Server
	gamepads *Gamepads
	ring     *frameRing

	// Each with its own lock; see the note on mu.
	timings  frameTimings
	captures captures
	states   stateProviders
}

// Server returns the MCP server serving this game, or nil when none was
// started.
func (r *Runtime) Server() *Server {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.server
}

// Close stops the MCP server, puts stdout and stderr back, drops the frame
// buffer and unplugs any virtual controllers. The game is untouched.
//
// Two of those outlive the process if they are skipped, which is why this is
// not optional and why RunGame and RunTests both call it:
//
//   - A uinput device belongs to the kernel, not to the process that asked for
//     it, so leaving without destroying one leaves a phantom gamepad on the
//     machine.
//   - Trace capture redirects descriptors 1 and 2 through a pipe. Until they are
//     put back, everything written to them reaches the terminal only by way of a
//     goroutine — and os.Exit does not wait for goroutines, so whatever was
//     written last is lost. For a test binary that is the failure message.
//
// Safe to call twice; the second one has nothing to do.
func (r *Runtime) Close() error {
	r.mu.Lock()
	gamepads, ring, server := r.gamepads, r.ring, r.server
	r.gamepads, r.ring, r.server = nil, nil, nil
	r.mu.Unlock()

	if gamepads != nil {
		gamepads.Close()
	}
	if ring != nil {
		ring.Disable()
	}

	var err error
	if server != nil {
		err = server.Close()
		server.traces.stop()
	}
	return err
}

func newRuntime(game ebiten.Game) *Runtime {
	return &Runtime{
		game:     game,
		injector: hook.NewInjector(),
		started:  time.Now(),
		lastTick: time.Now(),
		tickCh:   make(chan struct{}),
		commands: make(chan func(), 64),
		reads:    make(chan func(), 64),
		captures: captures{stage: StageFinal},
	}
}

// Do runs fn inside the game loop, at the point the game's own Update would
// run, and waits for it to finish.
//
// Every caller must pass a deadline. A game that has crashed or deadlocked
// never drains the queue, and a debugging tool that hangs along with the thing
// it is debugging is worse than useless.
func (r *Runtime) Do(ctx context.Context, fn func()) error {
	return r.do(ctx, r.commands, fn)
}

// doAfterInput runs fn inside the loop too, but after the injector has written
// this tick's synthetic input rather than before.
//
// Which side of that line a caller wants is not a detail. Ebitengine rebuilds
// the game-visible input state at the top of every tick from what the window
// actually reported, which on a machine nobody is touching is nothing at all;
// the injector writes over that, once a tick, from the wrapper's Update. So
// anything asking what is pressed has to ask after that write. Before it, the
// honest answer is always "nothing", whatever was injected and whatever the
// game is about to see — which is what game_input_state answered for its whole
// life.
//
// Commands stay on the other side, and for the mirror-image reason: one may
// have just asked for a key to be held, and it has to be held before the write
// that makes the game see it.
func (r *Runtime) doAfterInput(ctx context.Context, fn func()) error {
	return r.do(ctx, r.reads, fn)
}

func (r *Runtime) do(ctx context.Context, queue chan func(), fn func()) error {
	done := make(chan struct{})
	var raised *CommandPanic

	// The recover is registered second, so it runs first: whatever it captures
	// is written before done is closed, and the channel carries it across.
	command := func() {
		defer close(done)

		// Checked here, at the last moment before running.
		//
		// A command that timed out is still sitting in the queue: the caller has
		// already been told the loop was stalled and has gone, and if the loop
		// then recovers, the mutation lands anyway. A game_reset or a game_step
		// arriving that way is a change nobody is expecting and nothing explains
		// — which is worse than the failure the caller was already told about.
		if ctx.Err() != nil {
			return
		}

		defer func() {
			if v := recover(); v != nil {
				raised = &CommandPanic{Value: fmt.Sprint(v), Stack: string(debug.Stack())}
			}
		}()

		fn()
	}

	select {
	case queue <- command:
	case <-ctx.Done():
		return ErrLoopStalled
	}

	select {
	case <-done:
		if raised != nil {
			return raised
		}
		return nil
	case <-ctx.Done():
		return ErrLoopStalled
	}
}

// injectEvent writes input from inside the game loop, at the point the commands
// are drained — which is the step before the injector is applied.
//
// State-like input does not need this. A key held down or a cursor moved stays
// where it was put until something moves it, so landing a tick late costs a
// tick and nothing else. Events do: runes and the wheel are handed to the game
// for exactly one tick and then dropped, which is what makes them behave like
// the real thing.
//
// Written from any goroutine but the loop's, an event lands at whatever point
// of the frame that goroutine happens to get. Apply runs at a fixed one, so a
// write that arrives after it is held back to the next tick — while the caller
// waits the one tick the event is supposed to need, reads, and finds nothing,
// having done everything right. Draining the commands happens before Apply, so
// writing from in there is always in time.
func (r *Runtime) injectEvent(ctx context.Context, fn func(*hook.Injector)) error {
	return r.Do(ctx, func() { fn(r.inject()) })
}

// CommandPanic is what a caller gets when the work it asked to run inside the
// game loop panicked.
//
// It exists so that the panic is the caller's problem rather than everybody's.
// Without it the panic unwinds through the loop and kills the process — which
// game_inspect can do just by reading, since reflection over a value taken from
// an unexported field panics on Interface(). A tool marked read-only bringing
// down the game it is inspecting is the worst outcome available here; being told
// what went wrong, by a game that is still running, is much the best.
type CommandPanic struct {
	Value string
	Stack string
}

func (e *CommandPanic) Error() string {
	return "ebitenmcp: what you asked to run inside the game loop panicked: " + e.Value
}

// WaitTicks blocks until the game has advanced n ticks. A paused game advances
// none, so this is also how a caller notices it is paused.
//
// It counts against a target tick rather than counting wake-ups, and that is not
// a detail. Waiting for the channel n times loses any tick that lands between
// one wake-up and the next read of the channel, which with vsync off is most of
// them — so waiting for four hundred ticks would sit there long after four
// hundred had gone by, and report a stalled loop that had done exactly what it
// was asked. Reading the tick and the channel under the same lock closes that:
// a tick that arrives in between has either already been counted or will close
// the channel now held.
func (r *Runtime) WaitTicks(ctx context.Context, n int) error {
	r.mu.Lock()
	target := r.tick + int64(n)
	r.mu.Unlock()

	for {
		r.mu.Lock()
		tick, ch := r.tick, r.tickCh
		r.mu.Unlock()

		if tick >= target {
			return nil
		}

		select {
		case <-ch:
		case <-ctx.Done():
			return ErrLoopStalled
		}
	}
}

// Tick is the number of ticks the wrapped game has actually run. It is not
// Ebitengine's tick: a paused game keeps being ticked by the engine while this
// counter stands still, which is what makes stepping meaningful.
func (r *Runtime) Tick() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.tick
}

// LastTick is when the game last completed a tick. Compared against now, it is
// the cheapest way to tell a paused game from a wedged one.
func (r *Runtime) LastTick() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.lastTick
}

// Uptime is how long the game has been running.
func (r *Runtime) Uptime() time.Duration {
	return time.Since(r.started)
}

// Pause stops calling the game's Update. Draw keeps running, so the last frame
// stays on screen and can still be captured.
func (r *Runtime) Pause() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.paused = true
	r.steps = 0
}

// Resume undoes Pause.
func (r *Runtime) Resume() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.paused = false
	r.steps = 0
}

// Step runs exactly n more ticks and pauses again.
func (r *Runtime) Step(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.paused = true
	r.steps += n
}

// Paused reports whether the game is currently held.
func (r *Runtime) Paused() (paused bool, steps int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.paused, r.steps
}

// Terminate ends the game loop cleanly, as if the game had returned
// ebiten.Termination.
//
// It deliberately ignores pause, steps and a recorded crash: a game held or
// wedged in any of those states still has to be able to shut down, and it is
// the only way out for a test binary that owns the process's single loop.
func (r *Runtime) Terminate() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.terminating = true
}

// Crash returns the panic the game died of, or nil if it is still alive.
func (r *Runtime) Crash() *Crash {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.crash
}

// Injector is the handle used to synthesise input. It does nothing when
// injection is unavailable; InputError says why.
func (r *Runtime) Injector() Injector { return Injector{r.inject()} }

// inject is the same handle without the wrapper, for the code in this package
// that needs the whole thing — the queue's Apply and Idle, and the touch and
// rune types the tools already hold.
func (r *Runtime) inject() *hook.Injector {
	return r.injector
}

// InputError reports why input injection is unavailable, or nil when it works.
// It is only meaningful after the first tick, which is when the layout
// self-check runs.
func (r *Runtime) InputError() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.inputErr
}

// Timings returns the most recent frame timings, oldest first.
func (r *Runtime) Timings() []FrameTiming { return r.timings.recent() }

// SetGame replaces the running game with a new one at the next tick boundary.
//
// This exists because ebiten.RunGame cannot be called twice in a process, so
// "start over" cannot mean restarting the loop. Swapping the game the wrapper
// holds is the only way to get a fresh one, and it is what makes independent
// tests possible inside a single test binary.
func (r *Runtime) SetGame(ctx context.Context, game ebiten.Game) error {
	if err := r.canReplace(game); err != nil {
		return err
	}

	return r.Do(ctx, func() {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.game = game
		r.crash = nil
	})
}

// canReplace refuses a game Ebitengine could not see properly.
//
// Go cannot implement an interface conditionally, so the wrapper's concrete type
// is chosen once, from the first game, and Ebitengine decides what to call by
// asserting on that type. A replacement that implements FinalScreenDrawer where
// the first did not would simply never have it called — its final pass would not
// run, and every capture would quietly be of something the player never sees.
// The other direction leaves the wrapper calling into an interface that is no
// longer there.
//
// Saying so is the only honest option: nothing here can change the type of an
// object Ebitengine is already holding.
func (r *Runtime) canReplace(game ebiten.Game) error {
	_, layoutF := game.(ebiten.LayoutFer)
	_, finalScreen := game.(ebiten.FinalScreenDrawer)

	r.mu.Lock()
	wantLayoutF, wantFinal := r.hasLayoutF, r.captures.finalPass()
	r.mu.Unlock()

	if layoutF == wantLayoutF && finalScreen == wantFinal {
		return nil
	}

	return fmt.Errorf("this game implements LayoutFer=%v FinalScreenDrawer=%v and the one it would "+
		"replace implements %v and %v; Ebitengine picked what to call from the first game and "+
		"cannot be told otherwise, so the mismatched half would silently never run. Make every "+
		"game the factory builds implement the same set",
		layoutF, finalScreen, wantLayoutF, wantFinal)
}

// Reset replaces the running game with a new one from the factory given at
// construction. It fails if there is no factory.
func (r *Runtime) Reset(ctx context.Context) error {
	r.mu.Lock()
	factory := r.factory
	r.mu.Unlock()

	if factory == nil {
		return errors.New("ebitenmcp: no game factory was configured, so there is nothing to reset to")
	}
	return r.SetGame(ctx, factory())
}

// currentGame returns the game being wrapped right now, which SetGame may have
// replaced since the wrapper was built.
func (r *Runtime) currentGame() ebiten.Game {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.game
}

// recordDraw and addDraw are the wrapper's way into the timing ring; see
// timings.go for why it is not part of this struct's state.
func (r *Runtime) recordDraw(d time.Duration) { r.timings.setDraw(d) }

func (r *Runtime) addDraw(d time.Duration) { r.timings.addDraw(d) }

// drainCommands runs everything queued, but only what was already waiting when
// it started. A command that queues another one therefore cannot starve the
// game of its tick.
func (r *Runtime) drainCommands() { drain(r.commands) }

// drainReads runs what was queued to see this tick's input, and so runs after
// the injector has written it.
func (r *Runtime) drainReads() { drain(r.reads) }

// drain runs what is queued now and no more. Taking the length first is what
// stops a command that queues another from being run in the same tick, which
// would let a loop of them hold the game still.
func drain(queue chan func()) {
	for n := len(queue); n > 0; n-- {
		select {
		case fn := <-queue:
			fn()
		default:
			return
		}
	}
}

// terminated reports whether Terminate was called.
func (r *Runtime) terminated() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.terminating
}

// shouldUpdate decides whether the game gets this tick, consuming one step if
// it is stepping.
func (r *Runtime) shouldUpdate() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.crash != nil {
		return false
	}
	if !r.paused {
		return true
	}
	if r.steps > 0 {
		r.steps--
		return true
	}
	return false
}

// advance records that a tick completed and wakes everyone waiting on one.
func (r *Runtime) advance(update, draw, wall time.Duration) {
	r.mu.Lock()
	r.tick++
	r.lastTick = time.Now()
	tick := r.tick

	// Closed and replaced every tick, whether or not anybody is waiting. That
	// looks wasteful and is not worth avoiding: an empty-struct channel is about
	// a hundred bytes, so sixty a second is a few kilobytes, and the alternative
	// is counting waiters — a protocol whose failure mode is a wake-up that never
	// arrives. WaitTicks has already had one of those.
	close(r.tickCh)
	r.tickCh = make(chan struct{})
	r.mu.Unlock()

	// Outside the lock, and with its own: the timing ring is not loop state and
	// nothing that reads it needs the loop to be still.
	r.timings.add(tick, update, draw, wall)
}

// recordCrash stores a panic and stops the game being called again.
func (r *Runtime) recordCrash(phase string, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.crash != nil {
		return
	}

	r.crash = &Crash{
		Value: fmt.Sprint(v),
		Stack: string(debug.Stack()),
		Tick:  r.tick,
		When:  time.Now(),
		Phase: phase,
	}
}

// verifyInput runs the layout self-check once, before anything is injected.
func (r *Runtime) verifyInput() {
	r.mu.Lock()
	if r.verified {
		r.mu.Unlock()
		return
	}
	r.verified = true
	r.mu.Unlock()

	err := hook.Verify()

	r.mu.Lock()
	r.inputErr = err
	r.mu.Unlock()
}
