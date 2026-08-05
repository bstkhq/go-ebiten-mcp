package ebitenmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
func (s *Server) finish(ctx context.Context, after afterInput, out InputOutput) (*mcpsdk.CallToolResult, InputOutput, error) {
	if after.ThenWaitTicks > 0 {
		waitCtx, cancel := tickBudget(ctx, after.ThenWaitTicks)
		defer cancel()

		if err := s.rt.WaitTicks(waitCtx, after.ThenWaitTicks); err != nil {
			return nil, InputOutput{}, s.stalled(err)
		}
	}

	out.Tick = s.rt.Tick()

	if !after.ThenScreenshot {
		return nil, out, nil
	}

	shotCtx, cancel := withTimeout(ctx)
	defer cancel()

	frame, err := s.rt.Capture(shotCtx)
	if err != nil {
		return nil, InputOutput{}, s.stalled(err)
	}

	result, _, err := s.frameResult("shot", frame, 0, false, describe(out))
	return result, out, err
}

// describe puts what the call did on the picture it returns, so a contact sheet
// or a chat log says what was pressed rather than only showing the aftermath.
func describe(out InputOutput) string {
	data, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(data)
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

func (s *Server) key(ctx context.Context, _ *mcpsdk.CallToolRequest, in keyInput) (*mcpsdk.CallToolResult, InputOutput, error) {
	if err := s.requireInput(); err != nil {
		return nil, InputOutput{}, err
	}

	keys, err := parseKeys(in.Keys)
	if err != nil {
		return nil, InputOutput{}, err
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

		holdCtx, cancel := tickBudget(ctx, in.Ticks)
		defer cancel()

		if err := s.rt.WaitTicks(holdCtx, in.Ticks); err != nil {
			inj.ReleaseAll()
			return nil, InputOutput{}, s.stalled(err)
		}

		for _, k := range keys {
			inj.KeyUp(k)
		}
	}

	out := InputOutput{Keys: in.Keys}
	if in.Hold {
		out.Held = in.Keys
	}
	if in.Release {
		out.Released = in.Keys
	}

	return s.finish(ctx, in.afterInput, out)
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

func (s *Server) typeText(ctx context.Context, _ *mcpsdk.CallToolRequest, in typeInput) (*mcpsdk.CallToolResult, InputOutput, error) {
	if err := s.requireInput(); err != nil {
		return nil, InputOutput{}, err
	}
	if in.Text == "" {
		return nil, InputOutput{}, fmt.Errorf("no text given")
	}

	s.rt.Injector().Type([]rune(in.Text))

	// Typed characters live for exactly one tick, like real ones, so the game
	// has to be allowed to run at least that tick before anything is reported.
	if in.ThenWaitTicks == 0 {
		in.ThenWaitTicks = 1
	}

	return s.finish(ctx, in.afterInput, InputOutput{Text: in.Text})
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

func (s *Server) mouse(ctx context.Context, _ *mcpsdk.CallToolRequest, in mouseInput) (*mcpsdk.CallToolResult, InputOutput, error) {
	if err := s.requireInput(); err != nil {
		return nil, InputOutput{}, err
	}

	button, err := parseButton(in.Button)
	if err != nil {
		return nil, InputOutput{}, err
	}

	inj := s.rt.Injector()
	var out InputOutput

	if in.Release {
		inj.ReleaseCursor()
		out.Cursor = "released"
	}

	if in.X != nil && in.Y != nil {
		inj.MoveCursor(*in.X, *in.Y)
		out.MovedTo = []float64{*in.X, *in.Y}
	}

	if in.ScrollX != 0 || in.ScrollY != 0 {
		inj.Scroll(in.ScrollX, in.ScrollY)
		out.Scrolled = []float64{in.ScrollX, in.ScrollY}
	}

	switch {
	case in.ToX != nil && in.ToY != nil:
		if in.X == nil || in.Y == nil {
			return nil, InputOutput{}, fmt.Errorf("a drag needs a starting x and y as well as to_x and to_y")
		}
		if err := s.drag(ctx, *in.X, *in.Y, *in.ToX, *in.ToY, button, in.Steps); err != nil {
			return nil, InputOutput{}, err
		}
		out.DraggedTo = []float64{*in.ToX, *in.ToY}

	case in.Click:
		if err := s.click(ctx, button); err != nil {
			return nil, InputOutput{}, err
		}
		out.Clicked = in.Button

	case in.Down:
		inj.MouseDown(button)
		out.Holding = in.Button

	case in.Up:
		inj.MouseUp(button)
		out.Button = in.Button
	}

	return s.finish(ctx, in.afterInput, out)
}

// drag walks the cursor across several ticks with the button held.
//
// Teleporting from one point to the other and clicking would produce a stroke
// of two points in a paint program and no hover events anywhere else, which is
// not what a drag looks like to a game.
// click presses and releases, holding for a tick in between so that a game
// polling IsMouseButtonPressed cannot miss it between two frames.
//
// Shared with game_script, which had its own copy of the same three lines and
// its own way of putting the button back on failure.
func (s *Server) click(ctx context.Context, button ebiten.MouseButton) error {
	inj := s.rt.Injector()

	clickCtx, cancel := withTimeout(ctx)
	defer cancel()

	inj.MouseDown(button)
	if err := s.rt.WaitTicks(clickCtx, 1); err != nil {
		inj.MouseUp(button)
		return s.stalled(err)
	}
	inj.MouseUp(button)

	return nil
}

// dragPath is the straight line a drag follows, as the points to visit.
//
// Split out because the test driver walks the same line and had its own copy of
// the arithmetic: two implementations of "what does a drag look like" is two
// things that can disagree about whether the last point is the destination.
func dragPath(x0, y0, x1, y1 float64, steps int) [][2]float64 {
	if steps < 1 {
		steps = 10
	}

	path := make([][2]float64, 0, steps)
	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		path = append(path, [2]float64{x0 + (x1-x0)*f, y0 + (y1-y0)*f})
	}
	return path
}

func (s *Server) drag(ctx context.Context, x0, y0, x1, y1 float64, button ebiten.MouseButton, steps int) error {
	if steps <= 0 {
		steps = 10
	}

	inj := s.rt.Injector()

	dragCtx, cancel := tickBudget(ctx, 2*(steps+4))
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

	for _, at := range dragPath(x0, y0, x1, y1, steps) {
		inj.MoveCursor(at[0], at[1])

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

func (s *Server) touch(ctx context.Context, _ *mcpsdk.CallToolRequest, in touchInput) (*mcpsdk.CallToolResult, InputOutput, error) {
	if err := s.requireInput(); err != nil {
		return nil, InputOutput{}, err
	}

	s.applyTouches(in.Touches)

	if in.ThenWaitTicks == 0 {
		in.ThenWaitTicks = 1
	}

	return s.finish(ctx, in.afterInput, InputOutput{Touches: len(in.Touches)})
}

// applyTouches replaces the set of active touches, shared with game_script so
// the two cannot drift apart.
func (s *Server) applyTouches(points []touchPoint) {
	touches := make([]hook.Touch, 0, len(points))
	for _, t := range points {
		touches = append(touches, hook.Touch{ID: t.ID, X: t.X, Y: t.Y})
	}

	s.rt.Injector().SetTouches(touches)
}

// ---------------------------------------------------------------------------
// game_input_state
// ---------------------------------------------------------------------------

func (s *Server) inputState(ctx context.Context, _ *mcpsdk.CallToolRequest, _ emptyInput) (*mcpsdk.CallToolResult, InputStateOutput, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	out := InputStateOutput{Injection: s.inputStatus()}

	// Read from inside the loop: input state is only coherent for the tick it
	// belongs to, and reading it from outside would mix two of them.
	if err := s.rt.Do(ctx, func() {
		cx, cy := ebiten.CursorPosition()
		wx, wy := ebiten.Wheel()

		out.KeysPressed = pressedKeys()
		out.MouseButtons = pressedMouseButtons()
		out.Touches = activeTouches()
		out.Gamepads = connectedGamepads()
		out.Cursor = Point{X: cx, Y: cy}
		out.Wheel = Offset{X: wx, Y: wy}
	}); err != nil {
		return nil, InputStateOutput{}, s.stalled(err)
	}

	out.Tick = s.rt.Tick()
	return nil, out, nil
}

func pressedKeys() []string {
	var keys []string
	for _, k := range inpututil.AppendPressedKeys(nil) {
		keys = append(keys, k.String())
	}
	return keys
}

func pressedMouseButtons() []string {
	var names []string
	for _, b := range []ebiten.MouseButton{ebiten.MouseButtonLeft, ebiten.MouseButtonRight, ebiten.MouseButtonMiddle} {
		if ebiten.IsMouseButtonPressed(b) {
			names = append(names, buttonName(b))
		}
	}
	return names
}

func activeTouches() []touchPoint {
	var touches []touchPoint
	for _, id := range ebiten.AppendTouchIDs(nil) {
		x, y := ebiten.TouchPosition(id)
		touches = append(touches, touchPoint{ID: int(id), X: x, Y: y})
	}
	return touches
}

// connectedGamepads describes every pad the game can see.
//
// The SDL id is here because it is what a game switches on to decide what kind
// of controller this is, so seeing it is often the whole answer to "why is the
// game ignoring my gamepad".
func connectedGamepads() []GamepadState {
	var pads []GamepadState

	for _, id := range ebiten.AppendGamepadIDs(nil) {
		var pressed []int
		for b := 0; b < ebiten.GamepadButtonCount(id); b++ {
			if ebiten.IsGamepadButtonPressed(id, ebiten.GamepadButton(b)) {
				pressed = append(pressed, b)
			}
		}

		var axes []float64
		for a := 0; a < ebiten.GamepadAxisCount(id); a++ {
			axes = append(axes, ebiten.GamepadAxisValue(id, a))
		}

		pads = append(pads, GamepadState{
			ID:             int(id),
			Name:           ebiten.GamepadName(id),
			SDLID:          ebiten.GamepadSDLID(id),
			StandardLayout: ebiten.IsStandardGamepadLayoutAvailable(id),
			ButtonsPressed: pressed,
			Axes:           axes,
		})
	}
	return pads
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
