package ebitenmcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/hook"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addInputTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_key",
		Description: "Press keys, by name: space, arrowleft, a, escape. Held for the requested " +
			"number of ticks, so the game sees a real press with a real duration.",
		Annotations: mutating("Press keys"),
	}, s.key)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_type",
		Description: "Type text into the game. This goes in as characters rather than key " +
			"presses, which is the path a text field actually reads.",
		Annotations: mutating("Type text"),
	}, s.typeText)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_mouse",
		Description: "Move, click, drag or scroll. Coordinates are the game's own screen pixels, " +
			"the same ones it sees from ebiten.CursorPosition.",
		Annotations: mutating("Mouse"),
	}, s.mouse)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "game_touch",
		Description: "Place, move or lift touches. Several at once, each with its own id.",
		Annotations: mutating("Touch"),
	}, s.touch)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_input_state",
		Description: "What input the game is seeing right now. Use it to tell 'the key was never " +
			"injected' apart from 'the game ignored the key'.",
		Annotations: readOnly("Input state"),
	}, s.inputState)
}

// afterInput is embedded in every input tool.
//
// A press followed by a wait followed by a screenshot is the loop an agent
// spends all its time in, and doing it in one call rather than three is the
// difference between a pleasant tool and a tedious one.
type afterInput struct {
	ThenWaitTicks  int  `json:"then_wait_ticks,omitempty" jsonschema:"let the game run this many ticks afterwards"`
	ThenScreenshot bool `json:"then_screenshot,omitempty" jsonschema:"capture a frame afterwards and return it inline"`
}

// requireInput refuses politely rather than pretending to press something.
func (s *Server) requireInput() error {
	if !hook.Supported {
		return fmt.Errorf("this build has input injection compiled out (-tags ebitenmcp_nohook)")
	}
	if err := s.rt.InputError(); err != nil {
		return fmt.Errorf("input injection is unavailable: %w", err)
	}
	if s.rt.Tick() == 0 {
		return fmt.Errorf("the game has not run a tick yet, so input cannot be checked or delivered")
	}
	return nil
}

// finish applies the shared "and then" behaviour.
func (s *Server) finish(ctx context.Context, after afterInput, out map[string]any) (*mcpsdk.CallToolResult, any, error) {
	if after.ThenWaitTicks > 0 {
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(after.ThenWaitTicks)*100*time.Millisecond+defaultToolTimeout)
		defer cancel()

		if err := s.rt.WaitTicks(waitCtx, after.ThenWaitTicks); err != nil {
			return nil, nil, s.stalled(err)
		}
	}

	out["tick"] = s.rt.Tick()

	if !after.ThenScreenshot {
		return nil, out, nil
	}

	shotCtx, cancel := withTimeout(ctx)
	defer cancel()

	frame, err := s.rt.Capture(shotCtx)
	if err != nil {
		return nil, nil, s.stalled(err)
	}

	result, _, err := s.frameResult("shot", frame, 0, false, describe(out))
	return result, out, err
}

func describe(out map[string]any) string {
	parts := make([]string, 0, len(out))
	for k, v := range out {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// game_key
// ---------------------------------------------------------------------------

type keyInput struct {
	afterInput

	Keys    []string `json:"keys" jsonschema:"key names, case-insensitive: space, enter, arrowleft, a, digit1, shift"`
	Ticks   int      `json:"ticks,omitempty" jsonschema:"how many ticks to hold them for; defaults to 1"`
	Hold    bool     `json:"hold,omitempty" jsonschema:"hold them down and do not release, until a later call with release"`
	Release bool     `json:"release,omitempty" jsonschema:"release keys held by an earlier call"`
}

func (s *Server) key(ctx context.Context, _ *mcpsdk.CallToolRequest, in keyInput) (*mcpsdk.CallToolResult, any, error) {
	if err := s.requireInput(); err != nil {
		return nil, nil, err
	}

	keys, err := parseKeys(in.Keys)
	if err != nil {
		return nil, nil, err
	}

	inj := s.rt.Injector()

	switch {
	case in.Release:
		for _, k := range keys {
			inj.KeyUp(k)
		}

	case in.Hold:
		for _, k := range keys {
			inj.KeyDown(k)
		}

	default:
		if in.Ticks <= 0 {
			in.Ticks = 1
		}

		for _, k := range keys {
			inj.KeyDown(k)
		}

		holdCtx, cancel := context.WithTimeout(ctx, time.Duration(in.Ticks)*100*time.Millisecond+defaultToolTimeout)
		defer cancel()

		if err := s.rt.WaitTicks(holdCtx, in.Ticks); err != nil {
			inj.ReleaseAll()
			return nil, nil, s.stalled(err)
		}

		for _, k := range keys {
			inj.KeyUp(k)
		}
	}

	return s.finish(ctx, in.afterInput, map[string]any{
		"keys":  in.Keys,
		"held":  in.Hold,
		"ticks": in.Ticks,
	})
}

// parseKeys uses Ebitengine's own key table, so the names a caller can use are
// exactly the names Ebitengine documents. Inventing a second vocabulary here
// would be one more thing to get out of step.
func parseKeys(names []string) ([]ebiten.Key, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no keys given")
	}

	keys := make([]ebiten.Key, 0, len(names))
	for _, name := range names {
		var k ebiten.Key
		if err := k.UnmarshalText([]byte(strings.TrimSpace(name))); err != nil {
			return nil, fmt.Errorf("unknown key %q; Ebitengine's names are things like Space, ArrowLeft, A, Digit1, ShiftLeft", name)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// ---------------------------------------------------------------------------
// game_type
// ---------------------------------------------------------------------------

type typeInput struct {
	afterInput

	Text string `json:"text" jsonschema:"the text to type"`
}

func (s *Server) typeText(ctx context.Context, _ *mcpsdk.CallToolRequest, in typeInput) (*mcpsdk.CallToolResult, any, error) {
	if err := s.requireInput(); err != nil {
		return nil, nil, err
	}
	if in.Text == "" {
		return nil, nil, fmt.Errorf("no text given")
	}

	s.rt.Injector().Type([]rune(in.Text))

	// Typed characters live for exactly one tick, like real ones, so the game
	// has to be allowed to run at least that tick before anything is reported.
	if in.ThenWaitTicks == 0 {
		in.ThenWaitTicks = 1
	}

	return s.finish(ctx, in.afterInput, map[string]any{"typed": in.Text})
}

// ---------------------------------------------------------------------------
// game_mouse
// ---------------------------------------------------------------------------

type mouseInput struct {
	afterInput

	X       *float64 `json:"x,omitempty" jsonschema:"cursor x in the game's screen pixels"`
	Y       *float64 `json:"y,omitempty" jsonschema:"cursor y in the game's screen pixels"`
	ToX     *float64 `json:"to_x,omitempty" jsonschema:"drag to this x, with the button held all the way"`
	ToY     *float64 `json:"to_y,omitempty" jsonschema:"drag to this y"`
	Button  string   `json:"button,omitempty" jsonschema:"left, right or middle; defaults to left when clicking or dragging"`
	Click   bool     `json:"click,omitempty" jsonschema:"press and release at the position"`
	Down    bool     `json:"down,omitempty" jsonschema:"press and keep holding"`
	Up      bool     `json:"up,omitempty" jsonschema:"release a held button"`
	Steps   int      `json:"steps,omitempty" jsonschema:"how many ticks a drag is spread over; defaults to 10"`
	ScrollX float64  `json:"scroll_x,omitempty" jsonschema:"horizontal wheel movement"`
	ScrollY float64  `json:"scroll_y,omitempty" jsonschema:"vertical wheel movement"`
	Release bool     `json:"release_cursor,omitempty" jsonschema:"stop pinning the cursor and hand it back to the real pointer"`
}

func (s *Server) mouse(ctx context.Context, _ *mcpsdk.CallToolRequest, in mouseInput) (*mcpsdk.CallToolResult, any, error) {
	if err := s.requireInput(); err != nil {
		return nil, nil, err
	}

	button, err := parseButton(in.Button)
	if err != nil {
		return nil, nil, err
	}

	inj := s.rt.Injector()
	out := map[string]any{}

	if in.Release {
		inj.ReleaseCursor()
		out["cursor"] = "released"
	}

	if in.X != nil && in.Y != nil {
		inj.MoveCursor(*in.X, *in.Y)
		out["moved_to"] = []float64{*in.X, *in.Y}
	}

	if in.ScrollX != 0 || in.ScrollY != 0 {
		inj.Scroll(in.ScrollX, in.ScrollY)
		out["scrolled"] = []float64{in.ScrollX, in.ScrollY}
	}

	switch {
	case in.ToX != nil && in.ToY != nil:
		if in.X == nil || in.Y == nil {
			return nil, nil, fmt.Errorf("a drag needs a starting x and y as well as to_x and to_y")
		}
		if err := s.drag(ctx, *in.X, *in.Y, *in.ToX, *in.ToY, button, in.Steps); err != nil {
			return nil, nil, err
		}
		out["dragged_to"] = []float64{*in.ToX, *in.ToY}

	case in.Click:
		inj.MouseDown(button)

		clickCtx, cancel := withTimeout(ctx)
		defer cancel()

		if err := s.rt.WaitTicks(clickCtx, 1); err != nil {
			inj.MouseUp(button)
			return nil, nil, s.stalled(err)
		}
		inj.MouseUp(button)
		out["clicked"] = in.Button

	case in.Down:
		inj.MouseDown(button)
		out["holding"] = in.Button

	case in.Up:
		inj.MouseUp(button)
		out["released"] = in.Button
	}

	return s.finish(ctx, in.afterInput, out)
}

// drag walks the cursor across several ticks with the button held.
//
// Teleporting from one point to the other and clicking would produce a stroke
// of two points in a paint program and no hover events anywhere else, which is
// not what a drag looks like to a game.
func (s *Server) drag(ctx context.Context, x0, y0, x1, y1 float64, button ebiten.MouseButton, steps int) error {
	if steps <= 0 {
		steps = 10
	}

	inj := s.rt.Injector()

	dragCtx, cancel := context.WithTimeout(ctx, time.Duration(steps+4)*200*time.Millisecond)
	defer cancel()

	inj.MoveCursor(x0, y0)
	if err := s.rt.WaitTicks(dragCtx, 1); err != nil {
		return s.stalled(err)
	}

	inj.MouseDown(button)
	if err := s.rt.WaitTicks(dragCtx, 1); err != nil {
		inj.MouseUp(button)
		return s.stalled(err)
	}

	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		inj.MoveCursor(x0+(x1-x0)*f, y0+(y1-y0)*f)

		if err := s.rt.WaitTicks(dragCtx, 1); err != nil {
			inj.MouseUp(button)
			return s.stalled(err)
		}
	}

	inj.MouseUp(button)
	return s.rt.WaitTicks(dragCtx, 1)
}

func parseButton(name string) (ebiten.MouseButton, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "left":
		return ebiten.MouseButtonLeft, nil
	case "right":
		return ebiten.MouseButtonRight, nil
	case "middle":
		return ebiten.MouseButtonMiddle, nil
	default:
		return 0, fmt.Errorf("unknown mouse button %q: left, right or middle", name)
	}
}

// ---------------------------------------------------------------------------
// game_touch
// ---------------------------------------------------------------------------

type touchPoint struct {
	ID int `json:"id" jsonschema:"identifies this finger across calls"`
	X  int `json:"x"`
	Y  int `json:"y"`
}

type touchInput struct {
	afterInput

	Touches []touchPoint `json:"touches" jsonschema:"the touches that are currently down; an empty list lifts them all"`
}

func (s *Server) touch(ctx context.Context, _ *mcpsdk.CallToolRequest, in touchInput) (*mcpsdk.CallToolResult, any, error) {
	if err := s.requireInput(); err != nil {
		return nil, nil, err
	}

	touches := make([]hook.Touch, 0, len(in.Touches))
	for _, t := range in.Touches {
		touches = append(touches, hook.Touch{ID: t.ID, X: t.X, Y: t.Y})
	}
	s.rt.Injector().SetTouches(touches)

	if in.ThenWaitTicks == 0 {
		in.ThenWaitTicks = 1
	}

	return s.finish(ctx, in.afterInput, map[string]any{"touches": len(touches)})
}

// ---------------------------------------------------------------------------
// game_input_state
// ---------------------------------------------------------------------------

func (s *Server) inputState(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, any, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	out := map[string]any{"injection": s.inputStatus()}

	// Read from inside the loop: input state is only coherent for the tick it
	// belongs to, and reading it from outside would mix two of them.
	if err := s.rt.Do(ctx, func() {
		var pressed []string
		for _, k := range inpututil.AppendPressedKeys(nil) {
			pressed = append(pressed, k.String())
		}

		var buttons []string
		for _, b := range []ebiten.MouseButton{ebiten.MouseButtonLeft, ebiten.MouseButtonRight, ebiten.MouseButtonMiddle} {
			if ebiten.IsMouseButtonPressed(b) {
				buttons = append(buttons, buttonName(b))
			}
		}

		var touches []touchPoint
		for _, id := range ebiten.AppendTouchIDs(nil) {
			x, y := ebiten.TouchPosition(id)
			touches = append(touches, touchPoint{ID: int(id), X: x, Y: y})
		}

		var gamepads []map[string]any
		for _, id := range ebiten.AppendGamepadIDs(nil) {
			var buttons []int
			for b := 0; b < ebiten.GamepadButtonCount(id); b++ {
				if ebiten.IsGamepadButtonPressed(id, ebiten.GamepadButton(b)) {
					buttons = append(buttons, b)
				}
			}

			var axes []float64
			for a := 0; a < ebiten.GamepadAxisCount(id); a++ {
				axes = append(axes, ebiten.GamepadAxisValue(id, a))
			}

			// The SDL id is here because it is what a game switches on to decide
			// what kind of controller this is, so seeing it is often the answer
			// to "why is the game ignoring my gamepad".
			gamepads = append(gamepads, map[string]any{
				"id":              int(id),
				"name":            ebiten.GamepadName(id),
				"sdl_id":          ebiten.GamepadSDLID(id),
				"standard_layout": ebiten.IsStandardGamepadLayoutAvailable(id),
				"buttons_pressed": buttons,
				"axes":            axes,
			})
		}

		cx, cy := ebiten.CursorPosition()
		wx, wy := ebiten.Wheel()

		out["gamepads"] = gamepads
		out["keys_pressed"] = pressed
		out["mouse_buttons"] = buttons
		out["cursor"] = map[string]int{"x": cx, "y": cy}
		out["wheel"] = map[string]float64{"x": wx, "y": wy}
		out["touches"] = touches
	}); err != nil {
		return nil, nil, s.stalled(err)
	}

	out["tick"] = s.rt.Tick()
	return nil, out, nil
}

func buttonName(b ebiten.MouseButton) string {
	switch b {
	case ebiten.MouseButtonLeft:
		return "left"
	case ebiten.MouseButtonRight:
		return "right"
	case ebiten.MouseButtonMiddle:
		return "middle"
	default:
		return fmt.Sprintf("button%d", b)
	}
}
