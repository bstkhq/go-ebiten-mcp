package ebitenmcp

import (
	"fmt"
	"os"

	"github.com/hajimehoshi/ebiten/v2"
)

// AddrEnv is the environment variable that turns the server on. Empty means no
// socket is opened and no goroutine is started, which is why leaving RunGame in
// a release build costs nothing. Setting it opens an unauthenticated port; bind
// loopback, and see the README before doing anything else with it.
const AddrEnv = "EBITEN_MCP_ADDR"

// CaptureEnv overrides which stage captures come from. Set it to "offscreen" in
// a game whose final pass is expensive and whose final pass you do not care
// about; see WithCaptureStage for why that is not the default.
const CaptureEnv = "EBITEN_MCP_CAPTURE"

// Options configures a wrapped game.
type Options struct {
	// Addr is the address the MCP server listens on. Defaults to
	// EBITEN_MCP_ADDR; empty means do not serve.
	Addr string

	// Name identifies this game to a client that finds several running.
	Name string

	// Factory builds a fresh game, which is what game_reset and the test driver
	// use to start over. Without it there is nothing to reset to, since
	// Ebitengine's loop cannot be restarted.
	Factory func() ebiten.Game

	// CaptureStage is which stage a capture comes from when it does not ask for
	// one. Defaults to StageFinal, and only means anything for a game that draws
	// its own final screen.
	CaptureStage Stage

	// States are named snapshots the game publishes, reachable as @name from
	// game_inspect. See WithState.
	States map[string]StateProvider
}

// Option customises Options.
type Option func(*Options)

// WithAddr overrides the listen address, ignoring the environment.
func WithAddr(addr string) Option {
	return func(o *Options) { o.Addr = addr }
}

// WithName sets the name reported to clients.
func WithName(name string) Option {
	return func(o *Options) { o.Name = name }
}

// WithFactory supplies a constructor for fresh games.
func WithFactory(factory func() ebiten.Game) Option {
	return func(o *Options) { o.Factory = factory }
}

// WithCaptureStage sets which stage captures come from by default.
//
// StageOffscreen skips the screen-sized copy the final pass needs, which is
// worth having when that pass is expensive and you are not debugging it. It is
// not the default, because returning the image from before a game's own final
// pass — without saying so — is returning something the player never saw.
func WithCaptureStage(stage Stage) Option {
	return func(o *Options) { o.CaptureStage = stage }
}

// WithState publishes a named snapshot of the game, reachable as @name from
// game_inspect. The provider is handed whatever game is running when it is
// called, so it keeps working across game_reset.
func WithState(name string, fn StateProvider) Option {
	return func(o *Options) {
		if o.States == nil {
			o.States = map[string]StateProvider{}
		}
		o.States[name] = fn
	}
}

func newOptions(opts []Option) *Options {
	o := &Options{
		Addr:         os.Getenv(AddrEnv),
		Name:         defaultName(),
		CaptureStage: captureStageFromEnv(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func captureStageFromEnv() Stage {
	if os.Getenv(CaptureEnv) == string(StageOffscreen) {
		return StageOffscreen
	}
	return StageFinal
}

func defaultName() string {
	if len(os.Args) > 0 {
		return baseName(os.Args[0])
	}
	return "game"
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

// Wrap returns the game to hand to Ebitengine and the runtime that drives it.
//
// Use it when the game is started by something other than RunGame — a custom
// runner, a mobile entry point, a test. RunGame is the same thing plus the call
// to ebiten.RunGame.
//
// The server starts here rather than in RunGame so that a custom runner gets it
// too. With no address configured nothing is started at all.
func Wrap(game ebiten.Game, opts ...Option) (ebiten.Game, *Runtime) {
	o := newOptions(opts)

	rt := newRuntime(game)
	rt.factory = o.Factory
	for name, fn := range o.States {
		rt.RegisterState(name, fn)
	}
	if o.CaptureStage != "" {
		rt.defaultStage = o.CaptureStage
	}

	if o.Addr != "" {
		server, err := Serve(rt, o)
		if err != nil {
			// A debug server that cannot bind must not stop the game from
			// running. Say so and carry on.
			fmt.Fprintf(os.Stderr, "ebitenmcp: not serving: %v\n", err)
		} else {
			rt.server = server
		}
	}

	return wrap(rt), rt
}

// RunGame is a drop-in replacement for ebiten.RunGame.
//
// The game runs exactly as it would have. With EBITEN_MCP_ADDR set it also
// serves MCP, so an agent can watch it, drive it and ask what it is doing.
func RunGame(game ebiten.Game, opts ...Option) error {
	return RunGameWithOptions(game, nil, opts...)
}

// RunGameWithOptions is a drop-in replacement for ebiten.RunGameWithOptions.
func RunGameWithOptions(game ebiten.Game, ebitenOptions *ebiten.RunGameOptions, opts ...Option) error {
	wrapped, rt := Wrap(game, opts...)
	defer rt.Close()

	return ebiten.RunGameWithOptions(wrapped, ebitenOptions)
}
