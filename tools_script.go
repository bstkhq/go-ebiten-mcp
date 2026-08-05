package ebitenmcp

import (
	"context"
	"fmt"

	"github.com/hajimehoshi/ebiten/v2"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addScriptTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_script",
		Description: "Run a sequence of input anchored to ticks, in one call. Reproducing a bug " +
			"otherwise means several calls with a wait between each, which is neither " +
			"repeatable nor something you can attach to an issue. Ticks are relative to the " +
			"start of the script, so the same script works whenever it is run. Returns a " +
			"contact sheet of the frames it was asked to capture.",
		Annotations: mutating("Run a script"),
	}, s.script)
}

// scriptStep is one moment in the sequence.
//
// The fields mirror the input tools rather than inventing a language: whatever
// game_key or game_mouse can do, a step can do, and learning one teaches the
// other.
type scriptStep struct {
	At int `json:"at" jsonschema:"tick to do this at, counted from the start of the script"`

	Keys    []string     `json:"keys,omitempty" jsonschema:"keys to press, by name"`
	Hold    int          `json:"hold,omitempty" jsonschema:"ticks to hold those keys for; defaults to 1"`
	Release []string     `json:"release,omitempty" jsonschema:"keys held by an earlier step, to let go of now"`
	Text    string       `json:"text,omitempty" jsonschema:"text to type"`
	Mouse   *mouseStep   `json:"mouse,omitempty" jsonschema:"a mouse move, click or drag"`
	Touches []touchPoint `json:"touches,omitempty" jsonschema:"touches to hold at this moment; an empty list lifts them"`

	Screenshot bool   `json:"screenshot,omitempty" jsonschema:"capture the frame here and include it in the contact sheet"`
	Label      string `json:"label,omitempty" jsonschema:"a note for this step, shown on the captured frame"`
}

type mouseStep struct {
	X      float64  `json:"x"`
	Y      float64  `json:"y"`
	ToX    *float64 `json:"to_x,omitempty" jsonschema:"drag to here"`
	ToY    *float64 `json:"to_y,omitempty"`
	Click  bool     `json:"click,omitempty"`
	Button string   `json:"button,omitempty" jsonschema:"left, right or middle"`
	Steps  int      `json:"steps,omitempty" jsonschema:"ticks a drag is spread over"`
}

type scriptInput struct {
	Steps   []scriptStep `json:"steps" jsonschema:"the sequence, in any order; they are run by tick"`
	Columns int          `json:"columns,omitempty" jsonschema:"columns in the returned contact sheet; defaults to 4"`
	MaxSize int          `json:"max_size,omitempty" jsonschema:"longest side of the inline contact sheet in pixels; defaults to 1024"`
}

func (s *Server) script(ctx context.Context, _ *mcpsdk.CallToolRequest, in scriptInput) (*mcpsdk.CallToolResult, any, error) {
	if err := s.requireInput(); err != nil {
		return nil, nil, err
	}
	if len(in.Steps) == 0 {
		return nil, nil, fmt.Errorf("a script needs steps")
	}
	if in.Columns <= 0 {
		in.Columns = 4
	}

	steps := sortedSteps(in.Steps)
	last := steps[len(steps)-1].At

	// The budget covers the whole sequence, doubled because a step can wait on
	// ticks of its own on top of the ones it is anchored to.
	ctx, cancel := tickBudget(ctx, 2*(last+10))
	defer cancel()

	start := s.rt.Tick()

	// Whatever this script pressed is let go of on the way out, however it
	// leaves. Releasing only on success — which is what this used to do — meant
	// a step that failed after pressing a key left it held, and the next tool
	// inherited it; the failure then looked like it belonged there. Only the
	// keys this script pressed, because the injector is shared and releasing
	// everything would drop somebody else's.
	held := map[ebiten.Key]bool{}
	defer func() {
		inj := s.rt.Injector()
		for k := range held {
			inj.KeyUp(k)
		}
	}()

	var (
		frames []*Frame
		log    []map[string]any
	)

	for i, step := range steps {
		if wait := int64(step.At) - (s.rt.Tick() - start); wait > 0 {
			if err := s.rt.WaitTicks(ctx, int(wait)); err != nil {
				return nil, nil, s.stalled(err)
			}
		}

		frame, err := s.runStep(ctx, step, held)
		if err != nil {
			// Stop rather than carry on. A sequence that continued past a step
			// that did not happen produces a result that means nothing, and
			// hides which step was the problem.
			return nil, nil, fmt.Errorf("step %d (at tick %d) failed: %w", i, step.At, err)
		}

		entry := map[string]any{"step": i, "at": step.At, "tick": s.rt.Tick()}
		if step.Label != "" {
			entry["label"] = step.Label
		}
		log = append(log, entry)

		if frame != nil {
			frames = append(frames, frame)
		}
	}

	out := map[string]any{
		"steps":    len(steps),
		"ticks":    s.rt.Tick() - start,
		"log":      log,
		"tick":     s.rt.Tick(),
		"captured": len(frames),
	}

	if len(frames) == 0 {
		return nil, out, nil
	}

	sheet := contactSheet(frames, in.Columns)

	art, err := s.media.savePNG("script", sheet)
	if err != nil {
		return nil, nil, err
	}
	out["contact_sheet"] = art

	note := fmt.Sprintf("%d steps over %d ticks, %d frames captured",
		len(steps), s.rt.Tick()-start, len(frames))

	result, _, err := imageResult(sheet, art, note, in.MaxSize, false)
	if err != nil {
		return nil, nil, err
	}
	return result, out, nil
}

// runStep performs one step and returns the frame if it asked for one.
func (s *Server) runStep(ctx context.Context, step scriptStep, held map[ebiten.Key]bool) (*Frame, error) {
	inj := s.rt.Injector()

	for _, name := range step.Release {
		keys, err := parseKeys([]string{name})
		if err != nil {
			return nil, err
		}
		inj.KeyUp(keys[0])
		delete(held, keys[0])
	}

	if len(step.Keys) > 0 {
		keys, err := parseKeys(step.Keys)
		if err != nil {
			return nil, err
		}

		hold := step.Hold
		if hold <= 0 {
			hold = 1
		}

		for _, k := range keys {
			inj.KeyDown(k)
			held[k] = true
		}
		if err := s.rt.WaitTicks(ctx, hold); err != nil {
			return nil, err
		}
		for _, k := range keys {
			inj.KeyUp(k)
			delete(held, k)
		}
	}

	if step.Text != "" {
		inj.Type([]rune(step.Text))
		if err := s.rt.WaitTicks(ctx, 1); err != nil {
			return nil, err
		}
	}

	if step.Touches != nil {
		s.applyTouches(step.Touches)
		if err := s.rt.WaitTicks(ctx, 1); err != nil {
			return nil, err
		}
	}

	if step.Mouse != nil {
		if err := s.runMouseStep(ctx, step.Mouse); err != nil {
			return nil, err
		}
	}

	if !step.Screenshot {
		return nil, nil
	}

	shotCtx, cancel := withTimeout(ctx)
	defer cancel()

	return s.rt.Capture(shotCtx)
}

func (s *Server) runMouseStep(ctx context.Context, m *mouseStep) error {
	button, err := parseButton(m.Button)
	if err != nil {
		return err
	}

	inj := s.rt.Injector()

	if m.ToX != nil && m.ToY != nil {
		return s.drag(ctx, m.X, m.Y, *m.ToX, *m.ToY, button, m.Steps)
	}

	inj.MoveCursor(m.X, m.Y)
	if err := s.rt.WaitTicks(ctx, 1); err != nil {
		return err
	}

	if !m.Click {
		return nil
	}

	if err := s.click(ctx, button); err != nil {
		return err
	}
	return s.rt.WaitTicks(ctx, 1)
}

// sortedSteps puts the sequence in tick order, so a script can be written in
// whatever order reads best.
func sortedSteps(steps []scriptStep) []scriptStep {
	out := make([]scriptStep, len(steps))
	copy(out, steps)

	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At < out[j-1].At; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
