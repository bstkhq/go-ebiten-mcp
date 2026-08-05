// Package ebitenmcp turns a running Ebitengine game into something an agent can
// see and drive: frames, state, traces and synthetic input, over MCP.
//
// The whole integration is one line, replacing ebiten.RunGame with this
// package's. Nothing happens until EBITEN_MCP_ADDR is set, so the call can stay
// in a production build without opening a port or starting a goroutine.
package ebitenmcp

import (
	"context"
	"errors"
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
	Phase string    `json:"phase"` // "update" or "draw"
}

// FrameTiming is what one tick cost.
type FrameTiming struct {
	Tick   int64 `json:"tick"`
	Update int64 `json:"update_ns"`
	Draw   int64 `json:"draw_ns"`
	Wall   int64 `json:"wall_ns"`
}

const frameTimingHistory = 600

// Runtime is the state behind a wrapped game: the tick counter, the command
// queue, the input injector and the captures.
//
// Everything that reads or writes game state has to go through Do, which runs
// it inside Update. Everything that describes the runtime itself is readable
// from any goroutine at any time, on purpose: those are exactly the questions
// worth asking when the loop has stopped answering.
type Runtime struct {
	mu sync.Mutex

	game    ebiten.Game
	factory func() ebiten.Game

	injector *hook.Injector
	inputErr error
	verified bool

	started  time.Time
	tick     int64
	lastTick time.Time
	tickCh   chan struct{}

	paused      bool
	steps       int
	terminating bool

	crash *Crash

	timings  [frameTimingHistory]FrameTiming
	ntimings int

	commands chan func()

	captures  []chan *Frame
	lastFrame *Frame

	server   *Server
	gamepads *Gamepads
	ring     *frameRing
}

// Server returns the MCP server serving this game, or nil when none was
// started.
func (r *Runtime) Server() *Server {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.server
}

// Close stops the MCP server and unplugs any virtual controllers. The game is
// untouched.
//
// The controllers matter here: a uinput device outlives the process that made
// it, so leaving without destroying them leaves phantom gamepads on the machine.
func (r *Runtime) Close() error {
	r.mu.Lock()
	gamepads := r.gamepads
	r.mu.Unlock()

	if gamepads != nil {
		gamepads.Close()
	}

	r.mu.Lock()
	ring := r.ring
	r.mu.Unlock()

	if ring != nil {
		ring.Disable()
	}

	if s := r.Server(); s != nil {
		return s.Close()
	}
	return nil
}

func newRuntime(game ebiten.Game) *Runtime {
	return &Runtime{
		game:     game,
		injector: hook.NewInjector(),
		started:  time.Now(),
		lastTick: time.Now(),
		tickCh:   make(chan struct{}),
		commands: make(chan func(), 64),
	}
}

// Do runs fn inside the game loop, at the point the game's own Update would
// run, and waits for it to finish.
//
// Every caller must pass a deadline. A game that has crashed or deadlocked
// never drains the queue, and a debugging tool that hangs along with the thing
// it is debugging is worse than useless.
func (r *Runtime) Do(ctx context.Context, fn func()) error {
	done := make(chan struct{})

	select {
	case r.commands <- func() { defer close(done); fn() }:
	case <-ctx.Done():
		return ErrLoopStalled
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ErrLoopStalled
	}
}

// WaitTicks blocks until the game has advanced n ticks. A paused game advances
// none, so this is also how a caller notices it is paused.
func (r *Runtime) WaitTicks(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		r.mu.Lock()
		ch := r.tickCh
		r.mu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			return ErrLoopStalled
		}
	}
	return nil
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

// Injector is the handle used to synthesise input. It is nil-safe to use even
// when injection is unavailable; InputError says why nothing happens.
func (r *Runtime) Injector() *hook.Injector {
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
func (r *Runtime) Timings() []FrameTiming {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := r.ntimings
	if n > frameTimingHistory {
		n = frameTimingHistory
	}

	out := make([]FrameTiming, 0, n)
	for i := r.ntimings - n; i < r.ntimings; i++ {
		out = append(out, r.timings[i%frameTimingHistory])
	}
	return out
}

// SetGame replaces the running game with a new one at the next tick boundary.
//
// This exists because ebiten.RunGame cannot be called twice in a process, so
// "start over" cannot mean restarting the loop. Swapping the game the wrapper
// holds is the only way to get a fresh one, and it is what makes independent
// tests possible inside a single test binary.
func (r *Runtime) SetGame(ctx context.Context, game ebiten.Game) error {
	return r.Do(ctx, func() {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.game = game
		r.crash = nil
	})
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

// recordDraw fills in the draw half of the current tick's timing. Draw runs
// after Update, so the entry already exists by the time this is called.
func (r *Runtime) recordDraw(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.ntimings == 0 {
		return
	}
	r.timings[(r.ntimings-1)%frameTimingHistory].Draw = d.Nanoseconds()
}

// drainCommands runs everything queued, but only what was already waiting when
// it started. A command that queues another one therefore cannot starve the
// game of its tick.
func (r *Runtime) drainCommands() {
	for n := len(r.commands); n > 0; n-- {
		select {
		case fn := <-r.commands:
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
	defer r.mu.Unlock()

	r.tick++
	r.lastTick = time.Now()

	r.timings[r.ntimings%frameTimingHistory] = FrameTiming{
		Tick:   r.tick,
		Update: update.Nanoseconds(),
		Draw:   draw.Nanoseconds(),
		Wall:   wall.Nanoseconds(),
	}
	r.ntimings++

	close(r.tickCh)
	r.tickCh = make(chan struct{})
}

// recordCrash stores a panic and stops the game being called again.
func (r *Runtime) recordCrash(phase string, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.crash != nil {
		return
	}

	r.crash = &Crash{
		Value: sprint(v),
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
