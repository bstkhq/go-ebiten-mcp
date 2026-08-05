package ebitenmcp

import (
	"context"
	"fmt"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addStateTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_state",
		Description: "How the game and the process are doing: tick, framerate, resolution, " +
			"memory, and whether the loop is actually running. This one never touches the " +
			"game loop, so it still answers when the game is wedged or has crashed.",
		Annotations: readOnly("State"),
	}, s.state)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_inspect",
		Description: "Read the game's own state, including unexported fields. Give a path like " +
			"screens.player.pos to drill in, or none for the whole tree. Named snapshots " +
			"registered by the game are addressed as @name.",
		Annotations: readOnly("Inspect state"),
	}, s.inspect)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_traces",
		Description: "Lines the process wrote to stdout and stderr, tagged with the tick they " +
			"were written in. Captured at the file descriptor, so this includes output from " +
			"libraries and from C, not only from the game's own logger.",
		Annotations: readOnly("Traces"),
	}, s.tracesTool)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_frametimes",
		Description: "What each of the last ticks cost, with percentiles. update and draw are " +
			"the game's own time; wall is the real gap between ticks. When wall is far larger " +
			"than update plus draw, the time is going to the GPU or the driver rather than to " +
			"the game, because Draw only queues commands and rasterisation happens after it " +
			"returns. This is the tool for a game that stutters.",
		Annotations: readOnly("Frame times"),
	}, s.frametimes)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_goroutines",
		Description: "A goroutine dump, in full. When the loop has stopped answering, this says " +
			"what it is stuck on — and unlike game_profile it does not need the Go toolchain, " +
			"which is what makes it the one to reach for on a deployed machine.",
		Annotations: readOnly("Goroutines"),
	}, s.goroutines)
}

// ---------------------------------------------------------------------------
// game_state
// ---------------------------------------------------------------------------

func (s *Server) state(context.Context, *mcpsdk.CallToolRequest, emptyInput) (*mcpsdk.CallToolResult, any, error) {
	paused, steps := s.rt.Paused()
	sinceTick := time.Since(s.rt.LastTick())
	loop := s.loopState(paused, sinceTick)

	w, h := ebiten.WindowSize()

	out := map[string]any{
		"name":            s.opts.Name,
		"loop":            loop,
		"tick":            s.rt.Tick(),
		"since_last_tick": sinceTick.Round(time.Millisecond).String(),
		"uptime":          s.rt.Uptime().Round(time.Second).String(),
		"paused":          paused,
		"queued_steps":    steps,

		"tps":        ebiten.TPS(),
		"actual_tps": round2(ebiten.ActualTPS()),
		"actual_fps": round2(ebiten.ActualFPS()),
		"vsync":      ebiten.IsVsyncEnabled(),

		"window":              map[string]int{"width": w, "height": h},
		"device_scale_factor": round2(ebiten.Monitor().DeviceScaleFactor()),

		"input": s.inputStatus(),

		"go": goStats(),

		"state_providers": stateProviderNames(),
		"media_dir":       s.media.dir,
		"frame_ring":      s.rt.Ring().Status(),
	}

	if frame := s.rt.LastFrame(); frame != nil {
		b := frame.Image.Bounds()
		out["screen"] = map[string]any{"width": b.Dx(), "height": b.Dy(), "captured_at_tick": frame.Tick}
	}

	if crash := s.rt.Crash(); crash != nil {
		out["crash"] = crash

		// What the game printed on its way down, without having to ask for it
		// separately. The lines around a panic are usually the reason for it,
		// and needing a second call to see them is needing a second call at
		// exactly the moment somebody is in a hurry.
		if lines := s.crashTraces(crash); len(lines) > 0 {
			out["crash_traces"] = lines
		}
	}

	return nil, out, nil
}

// crashTracesWindow is how far back from the panic to look. A second at sixty
// ticks a second: enough for whatever led up to it, short enough that the
// answer is not mostly the game's ordinary chatter.
const crashTracesWindow = 60

func (s *Server) crashTraces(crash *Crash) []TraceLine {
	return filterTraces(s.traces.Lines(), "", "", crash.Tick-crashTracesWindow, 40)
}

// loopState is the one-word answer to "is this game alive", and the reason
// game_state is worth calling first: a stalled loop and a paused one look the
// same from outside and mean completely different things.
func (s *Server) loopState(paused bool, sinceTick time.Duration) string {
	switch {
	case s.rt.Crash() != nil:
		return "crashed"
	case paused:
		return "paused"
	case sinceTick > time.Second:
		return "stalled"
	default:
		return "running"
	}
}

func goStats() map[string]any {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	return map[string]any{
		"version":    runtime.Version(),
		"goroutines": runtime.NumGoroutine(),
		"heap_mb":    round2(float64(mem.HeapAlloc) / (1 << 20)),
		"gc_cycles":  mem.NumGC,
	}
}

func (s *Server) inputStatus() map[string]any {
	status := map[string]any{"available": true}

	if err := s.rt.InputError(); err != nil {
		status["available"] = false
		status["reason"] = err.Error()
	}
	if s.rt.Tick() == 0 {
		status["available"] = false
		status["reason"] = "not checked yet: the game has not run a tick"
	}
	return status
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// ---------------------------------------------------------------------------
// game_inspect
// ---------------------------------------------------------------------------

type inspectInput struct {
	Path     string `json:"path,omitempty" jsonschema:"dotted path into the game, for example screens.player.pos, or @name for a registered snapshot"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"how deep to walk; defaults to 6"`
	MaxItems int    `json:"max_items,omitempty" jsonschema:"how many elements to take from each slice or map; defaults to 50"`
}

func (s *Server) inspect(ctx context.Context, _ *mcpsdk.CallToolRequest, in inspectInput) (*mcpsdk.CallToolResult, any, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	limits := Inspect{MaxDepth: in.MaxDepth, MaxItems: in.MaxItems}

	var (
		value any
		err   error
	)

	// The walk runs inside the loop. Reading a live game's fields from another
	// goroutine would be a data race in the most literal sense, and the values
	// would be a mix of two ticks.
	if doErr := s.rt.Do(ctx, func() {
		if name, ok := strings.CutPrefix(in.Path, "@"); ok {
			name, rest, _ := strings.Cut(name, ".")

			snapshot, found := callStateProvider(name)
			if !found {
				err = fmt.Errorf("no registered snapshot %q; there is %s",
					name, strings.Join(stateProviderNames(), ", "))
				return
			}
			if rest == "" {
				value = limits.Value(snapshot)
			} else {
				value, err = limits.At(snapshot, rest)
			}
			return
		}

		value, err = limits.At(s.rt.currentGame(), in.Path)
	}); doErr != nil {
		return nil, nil, s.stalled(doErr)
	}

	if err != nil {
		return nil, nil, err
	}

	return nil, map[string]any{
		"tick":  s.rt.Tick(),
		"path":  in.Path,
		"value": value,
	}, nil
}

// ---------------------------------------------------------------------------
// game_traces
// ---------------------------------------------------------------------------

type tracesInput struct {
	Stream    string `json:"stream,omitempty" jsonschema:"stdout or stderr; both by default"`
	Contains  string `json:"contains,omitempty" jsonschema:"only lines containing this text"`
	SinceTick int64  `json:"since_tick,omitempty" jsonschema:"only lines written at or after this tick"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many lines to return, most recent last; defaults to 200"`
}

func (s *Server) tracesTool(_ context.Context, _ *mcpsdk.CallToolRequest, in tracesInput) (*mcpsdk.CallToolResult, any, error) {
	if in.Limit <= 0 {
		in.Limit = 200
	}

	lines := filterTraces(s.traces.Lines(), in.Stream, in.Contains, in.SinceTick, in.Limit)

	return nil, map[string]any{
		"count": len(lines),
		"lines": lines,
	}, nil
}

// ---------------------------------------------------------------------------
// game_frametimes
// ---------------------------------------------------------------------------

type frametimesInput struct {
	Last    int  `json:"last,omitempty" jsonschema:"how many recent ticks to summarise; defaults to all that are kept"`
	Verbose bool `json:"verbose,omitempty" jsonschema:"include every tick, not only the summary"`
}

func (s *Server) frametimes(_ context.Context, _ *mcpsdk.CallToolRequest, in frametimesInput) (*mcpsdk.CallToolResult, any, error) {
	timings := s.rt.Timings()
	if in.Last > 0 && in.Last < len(timings) {
		timings = timings[len(timings)-in.Last:]
	}

	if len(timings) == 0 {
		return nil, map[string]any{"ticks": 0}, nil
	}

	update := summarise(timings, func(t FrameTiming) int64 { return t.Update })
	draw := summarise(timings, func(t FrameTiming) int64 { return t.Draw })
	wall := summarise(timings, func(t FrameTiming) int64 { return t.Wall })

	out := map[string]any{
		"ticks":  len(timings),
		"update": update,
		"draw":   draw,
		"wall":   wall,

		// The rate the engine reports is the honest headline. Percentiles of the
		// gap between ticks are not: when the engine falls behind it runs a
		// burst of updates inside a single frame to catch up, so most gaps are
		// microseconds and a few are enormous, and the median of that says
		// nothing at all. The mean survives it, because it is elapsed time over
		// ticks either way.
		"actual_tps": round2(ebiten.ActualTPS()),
		"actual_fps": round2(ebiten.ActualFPS()),
		"reading":    reading(update, draw, wall),
	}
	if in.Verbose {
		out["timings"] = timings
	}
	return nil, out, nil
}

// reading says out loud where the time is going.
//
// Without it, "draw: 146us" alongside a game running at three ticks a second
// reads as a contradiction. It is not: Draw only queues commands, so the game's
// own cost and the cost of actually rasterising the frame are different numbers,
// and only one of them is in Draw.
//
// It compares against the mean gap between ticks rather than the median, for
// the catch-up reason above: the mean is total elapsed time over ticks, which is
// the tick period whether or not the engine is bunching updates.
func reading(update, draw, wall map[string]any) string {
	updateNs, ok1 := nanos(update["mean"])
	drawNs, ok2 := nanos(draw["mean"])
	w, ok3 := nanos(wall["mean"])
	if !ok1 || !ok2 || !ok3 || w == 0 {
		return ""
	}

	inGame := updateNs + drawNs
	if float64(inGame) > 0.6*float64(w) {
		return "the game's own update and draw account for most of each tick"
	}
	return fmt.Sprintf(
		"the game itself uses %s of a %s tick; the rest is the GPU or the driver rasterising the frame",
		time.Duration(inGame).Round(time.Microsecond), time.Duration(w).Round(time.Millisecond))
}

// nanos parses back a formatted duration, since summarise already rounded them
// for display.
func nanos(v any) (int64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	return int64(d), true
}

// summarise reports percentiles rather than an average. A game that stutters
// has a fine mean and a terrible p99, and the mean is what hides it.
func summarise(timings []FrameTiming, get func(FrameTiming) int64) map[string]any {
	values := make([]int64, 0, len(timings))
	var total int64

	for _, t := range timings {
		v := get(t)
		values = append(values, v)
		total += v
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })

	pct := func(p float64) string {
		i := int(float64(len(values)-1) * p)
		return time.Duration(values[i]).Round(time.Microsecond).String()
	}

	return map[string]any{
		"mean": time.Duration(total / int64(len(values))).Round(time.Microsecond).String(),
		"p50":  pct(0.50),
		"p95":  pct(0.95),
		"p99":  pct(0.99),
		"max":  time.Duration(values[len(values)-1]).Round(time.Microsecond).String(),
	}
}

// ---------------------------------------------------------------------------
// game_goroutines
// ---------------------------------------------------------------------------

type goroutinesInput struct {
	All bool `json:"all,omitempty" jsonschema:"include every goroutine rather than only those in the game and this server"`
}

func (s *Server) goroutines(_ context.Context, _ *mcpsdk.CallToolRequest, in goroutinesInput) (*mcpsdk.CallToolResult, any, error) {
	var sb strings.Builder

	profile := pprof.Lookup("goroutine")
	if err := profile.WriteTo(&sb, 2); err != nil {
		return nil, nil, err
	}

	dump := sb.String()
	if !in.All {
		dump = interestingGoroutines(dump)
	}

	return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: dump}},
		}, map[string]any{
			"count": runtime.NumGoroutine(),
		}, nil
}

// interestingGoroutines drops the ones that are always there and always idle,
// so a stuck game loop is not buried under the runtime's own bookkeeping.
func interestingGoroutines(dump string) string {
	var kept []string

	for _, block := range strings.Split(dump, "\n\n") {
		switch {
		case strings.Contains(block, "runtime.gopark") && strings.Contains(block, "net/http.(*conn)"):
			continue
		case strings.Contains(block, "os/signal.signal_recv"):
			continue
		}
		kept = append(kept, block)
	}
	return strings.Join(kept, "\n\n")
}
