package ebitenmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addLoopTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_pause",
		Description: "Stop calling the game's Update. Drawing continues, so the frozen frame " +
			"stays on screen and can still be captured.",
		Annotations: mutating("Pause"),
	}, s.pause)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "game_resume",
		Description: "Let the game run again after a pause or a step.",
		Annotations: mutating("Resume"),
	}, s.resume)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_step",
		Description: "Run exactly N ticks and pause again. This is how to look at one frame " +
			"at a time without the game moving underneath the questions.",
		Annotations: mutating("Step"),
	}, s.step)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "game_set_tps",
		Description: "Change how many ticks per second the game runs at. Use 0 for as fast as it will go.",
		Annotations: mutating("Set TPS"),
	}, s.setTPS)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_wait",
		Description: "Wait for the game to advance N ticks, or until a state path reaches a " +
			"value. Returns what it waited for and how long it took.",
		Annotations: readOnly("Wait"),
	}, s.wait)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_reset",
		Description: "Rebuild the game from scratch without restarting the process. Needs the " +
			"game to have been started with a factory.",
		Annotations: mutating("Reset"),
	}, s.reset)
}

type emptyInput struct{}

func (s *Server) pause(context.Context, *mcpsdk.CallToolRequest, emptyInput) (*mcpsdk.CallToolResult, LoopOutput, error) {
	s.rt.Pause()
	return nil, LoopOutput{Paused: true, Tick: s.rt.Tick()}, nil
}

func (s *Server) resume(context.Context, *mcpsdk.CallToolRequest, emptyInput) (*mcpsdk.CallToolResult, LoopOutput, error) {
	s.rt.Resume()
	return nil, LoopOutput{Paused: false, Tick: s.rt.Tick()}, nil
}

type stepInput struct {
	Ticks int `json:"ticks,omitempty" jsonschema:"how many ticks to run; defaults to 1"`
}

func (s *Server) step(ctx context.Context, _ *mcpsdk.CallToolRequest, in stepInput) (*mcpsdk.CallToolResult, LoopOutput, error) {
	if in.Ticks <= 0 {
		in.Ticks = 1
	}

	// Budgeted for the ticks asked for, not a flat five seconds: stepping four
	// hundred ticks is a legitimate request that used to fail on its own success.
	ctx, cancel := tickBudget(ctx, in.Ticks)
	defer cancel()

	before := s.rt.Tick()
	s.rt.Step(in.Ticks)

	if err := s.rt.WaitTicks(ctx, in.Ticks); err != nil {
		return nil, LoopOutput{}, s.stalled(err)
	}

	return nil, LoopOutput{
		Tick:   s.rt.Tick(),
		Ran:    s.rt.Tick() - before,
		Paused: true,
	}, nil
}

type setTPSInput struct {
	TPS int `json:"tps" jsonschema:"ticks per second; 0 means run as fast as the machine allows"`
}

func (s *Server) setTPS(_ context.Context, _ *mcpsdk.CallToolRequest, in setTPSInput) (*mcpsdk.CallToolResult, TPSOutput, error) {
	tps := in.TPS
	if tps <= 0 {
		tps = ebiten.SyncWithFPS
	}
	ebiten.SetTPS(tps)

	return nil, TPSOutput{TPS: ebiten.TPS()}, nil
}

type waitInput struct {
	Ticks   int     `json:"ticks,omitempty" jsonschema:"wait this many ticks"`
	Path    string  `json:"path,omitempty" jsonschema:"a game state path to watch, for example screens.player.pos.x"`
	Equals  *string `json:"equals,omitempty" jsonschema:"stop when the path's value, formatted as text, equals this. An empty string is a value like any other, which is why this is nullable: leave it out to not test equality"`
	Changed bool    `json:"changed,omitempty" jsonschema:"stop as soon as the path's value changes at all"`
	Timeout int     `json:"timeout_ticks,omitempty" jsonschema:"give up after this many ticks; defaults to 600"`
}

// wait is the difference between polling a game and asking it a question. A
// condition on a state path is what makes a test written through these tools
// deterministic rather than a sequence of sleeps.
func (s *Server) wait(ctx context.Context, _ *mcpsdk.CallToolRequest, in waitInput) (*mcpsdk.CallToolResult, WaitOutput, error) {
	if in.Path == "" {
		if in.Ticks <= 0 {
			in.Ticks = 1
		}

		ctx, cancel := tickBudget(ctx, in.Ticks)
		defer cancel()

		start := time.Now()
		if err := s.rt.WaitTicks(ctx, in.Ticks); err != nil {
			return nil, WaitOutput{}, s.stalled(err)
		}
		return nil, WaitOutput{
			Tick:   s.rt.Tick(),
			Waited: time.Since(start).String(),
		}, nil
	}

	if in.Equals == nil && !in.Changed {
		return nil, WaitOutput{}, fmt.Errorf("waiting on %s needs something to wait for: "+
			"equals, or changed", in.Path)
	}
	if in.Timeout <= 0 {
		in.Timeout = 600
	}

	ctx, cancel := tickBudget(ctx, in.Timeout)
	defer cancel()

	first, err := s.pathValue(ctx, in.Path)
	if err != nil {
		return nil, WaitOutput{}, err
	}

	// One more read than there are ticks: the loop below checks, then waits, so
	// without a check after the last wait a value that arrives on the very tick
	// the budget ends is reported as a timeout. Waiting for something and being
	// told it did not happen on the tick it did is the least useful answer this
	// tool could give.
	for i := 0; i <= in.Timeout; i++ {
		value, err := s.pathValue(ctx, in.Path)
		if err != nil {
			return nil, WaitOutput{}, err
		}

		switch {
		case in.Equals != nil && fmt.Sprint(value) == *in.Equals:
			return nil, WaitOutput{Tick: s.rt.Tick(), Value: value, Matched: "equals"}, nil
		case in.Changed && fmt.Sprint(value) != fmt.Sprint(first):
			return nil, WaitOutput{Tick: s.rt.Tick(), Value: value, Was: first, Matched: "changed"}, nil
		}

		if i == in.Timeout {
			break
		}

		if err := s.rt.WaitTicks(ctx, 1); err != nil {
			return nil, WaitOutput{}, s.stalled(err)
		}
	}

	return nil, WaitOutput{}, fmt.Errorf("%s did not %s within %d ticks",
		in.Path, condition(in), in.Timeout)
}

func condition(in waitInput) string {
	if in.Equals != nil {
		return fmt.Sprintf("reach %q", *in.Equals)
	}
	return "change"
}

// pathValue reads one path out of the game, inside the loop.
func (s *Server) pathValue(ctx context.Context, path string) (any, error) {
	var (
		value any
		err   error
	)

	if doErr := s.rt.Do(ctx, func() {
		value, err = Inspect{MaxDepth: 2}.At(s.rt.currentGame(), path)
	}); doErr != nil {
		return nil, s.stalled(doErr)
	}
	return value, err
}

func (s *Server) reset(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, ResetOutput, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	if err := s.rt.Reset(ctx); err != nil {
		return nil, ResetOutput{}, err
	}
	return nil, ResetOutput{Tick: s.rt.Tick(), Reset: true}, nil
}
