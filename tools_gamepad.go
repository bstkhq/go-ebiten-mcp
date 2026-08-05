package ebitenmcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bstkhq/go-ebiten-mcp/internal/uinput"
	"github.com/hajimehoshi/ebiten/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addGamepadTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_gamepad",
		Description: "Plug in a virtual controller and drive it. The identity is yours to " +
			"choose — name, vendor, product, version — because games routinely decide what a " +
			"controller is from its vendor id, and one that could only claim to be an Xbox pad " +
			"would take a different path through the game than its real hardware. Linux only: " +
			"it is a real kernel device, not a simulation.",
		Annotations: mutating("Gamepad"),
	}, s.gamepad)
}

// gamepadProfileInput is the identity and shape of the controller to create.
//
// The numbers are strings so they can be written the way they are documented —
// "0x045e" rather than 1118 — which is how anybody actually knows them. Plain
// decimal is accepted too.
type gamepadProfileInput struct {
	Name    string `json:"name,omitempty" jsonschema:"what the device calls itself. Note that a game sees this only when the identity is not one Ebitengine's controller database knows; for a known one the database's name wins"`
	Vendor  string `json:"vendor,omitempty" jsonschema:"USB vendor id, for example 0x045e for Microsoft. This is what a game switches on"`
	Product string `json:"product,omitempty" jsonschema:"USB product id"`
	Version string `json:"version,omitempty" jsonschema:"device version"`
	Bus     string `json:"bus,omitempty" jsonschema:"bus type; 0x03 is USB and is the default"`
}

type gamepadInput struct {
	afterInput

	Connect    *gamepadProfileInput `json:"connect,omitempty" jsonschema:"plug in a controller. An empty object gives an Xbox 360 pad, which has a standard layout"`
	Disconnect bool                 `json:"disconnect,omitempty" jsonschema:"unplug the controller"`
	ID         *int                 `json:"id,omitempty" jsonschema:"which controller, when more than one is plugged in"`

	Buttons map[string]bool    `json:"buttons,omitempty" jsonschema:"buttons to press or release by standard name: a, b, x, y, start, back, guide, leftshoulder, rightshoulder, leftstick, rightstick"`
	Axes    map[string]float64 `json:"axes,omitempty" jsonschema:"axes to move, from -1 to 1: leftx, lefty, rightx, righty, lefttrigger, righttrigger"`
	Dpad    string             `json:"dpad,omitempty" jsonschema:"up, down, left, right, upleft, upright, downleft, downright, or none"`

	RawButtons map[string]bool    `json:"raw_buttons,omitempty" jsonschema:"buttons by evdev code, for a controller whose layout has no standard names"`
	RawAxes    map[string]float64 `json:"raw_axes,omitempty" jsonschema:"axes by evdev code, in the range the profile declared"`
}

// Standard names to evdev codes. The names are SDL's, which is what the mappings
// and everybody's documentation use.
var (
	gamepadButtons = map[string]uint16{
		"a": uinput.BtnA, "b": uinput.BtnB, "x": uinput.BtnX, "y": uinput.BtnY,
		"leftshoulder": uinput.BtnTL, "rightshoulder": uinput.BtnTR,
		"back": uinput.BtnSelect, "start": uinput.BtnStart, "guide": uinput.BtnMode,
		"leftstick": uinput.BtnThumbL, "rightstick": uinput.BtnThumbR,
	}
	gamepadAxes = map[string]uint16{
		"leftx": uinput.AbsX, "lefty": uinput.AbsY, "lefttrigger": uinput.AbsZ,
		"rightx": uinput.AbsRX, "righty": uinput.AbsRY, "righttrigger": uinput.AbsRZ,
	}
	gamepadDpad = map[string][2]int32{
		"none": {0, 0}, "up": {0, -1}, "down": {0, 1}, "left": {-1, 0}, "right": {1, 0},
		"upleft": {-1, -1}, "upright": {1, -1}, "downleft": {-1, 1}, "downright": {1, 1},
	}
)

func (s *Server) gamepad(ctx context.Context, _ *mcpsdk.CallToolRequest, in gamepadInput) (*mcpsdk.CallToolResult, any, error) {
	if err := uinput.Available(); err != nil {
		return nil, nil, fmt.Errorf("no virtual controller here: %w", err)
	}

	ctx, cancel := withTimeout(ctx)
	defer cancel()

	pads := s.rt.Gamepads()
	out := map[string]any{}

	if in.Connect != nil {
		profile, err := in.Connect.profile()
		if err != nil {
			return nil, nil, err
		}

		id, err := pads.Connect(ctx, profile)
		if err != nil {
			return nil, nil, err
		}

		out["connected"] = id
		out["sdl_id"] = profile.SDLID()
		in.ID = intPtr(int(id))
	}

	id, err := s.gamepadID(pads, in.ID)
	if err != nil && (len(in.Buttons) > 0 || len(in.Axes) > 0 || in.Dpad != "" ||
		len(in.RawButtons) > 0 || len(in.RawAxes) > 0 || in.Disconnect) {
		return nil, nil, err
	}

	if err == nil {
		if err := s.applyGamepad(pads, id, in); err != nil {
			return nil, nil, err
		}
		out["id"] = id
	}

	if in.Disconnect {
		if err := pads.Disconnect(id); err != nil {
			return nil, nil, err
		}
		out["disconnected"] = id
		delete(out, "id")
	}

	return s.finish(ctx, in.afterInput, out)
}

// applyGamepad sends every change as one movement, so a diagonal push is a
// diagonal rather than two straight ones.
func (s *Server) applyGamepad(pads *Gamepads, id ebiten.GamepadID, in gamepadInput) error {
	if len(in.Buttons) == 0 && len(in.Axes) == 0 && in.Dpad == "" &&
		len(in.RawButtons) == 0 && len(in.RawAxes) == 0 {
		return nil
	}

	profile, err := pads.Profile(id)
	if err != nil {
		return err
	}

	buttons := map[uint16]bool{}
	axes := map[uint16]int32{}

	for name, pressed := range in.Buttons {
		code, ok := gamepadButtons[strings.ToLower(name)]
		if !ok {
			return fmt.Errorf("unknown button %q; the standard names are %s",
				name, names(gamepadButtons))
		}
		buttons[code] = pressed
	}
	for name, value := range in.Axes {
		code, ok := gamepadAxes[strings.ToLower(name)]
		if !ok {
			return fmt.Errorf("unknown axis %q; the standard names are %s", name, names(gamepadAxes))
		}
		axes[code] = scaleAxis(profile, code, value)
	}

	if in.Dpad != "" {
		hat, ok := gamepadDpad[strings.ToLower(in.Dpad)]
		if !ok {
			return fmt.Errorf("unknown dpad direction %q", in.Dpad)
		}
		axes[uinput.AbsHat0X], axes[uinput.AbsHat0Y] = hat[0], hat[1]
	}

	for code, pressed := range in.RawButtons {
		n, err := parseCode(code)
		if err != nil {
			return fmt.Errorf("raw button %q: %w", code, err)
		}
		buttons[n] = pressed
	}
	for code, value := range in.RawAxes {
		n, err := parseCode(code)
		if err != nil {
			return fmt.Errorf("raw axis %q: %w", code, err)
		}
		axes[n] = int32(value)
	}

	return pads.Apply(id, buttons, axes)
}

// scaleAxis turns -1..1 into whatever range the profile declared for that axis.
//
// A trigger runs 0..255 and a stick -32767..32767, so a caller asking for "all
// the way" cannot mean a number: it has to mean a proportion.
func scaleAxis(profile GamepadProfile, code uint16, value float64) int32 {
	value = clampAxis(value)

	for _, axis := range profile.Axes {
		if axis.Code != code {
			continue
		}
		if axis.Min >= 0 {
			// One-sided, like a trigger: -1 and 0 both mean released.
			if value < 0 {
				value = 0
			}
			return axis.Min + int32(value*float64(axis.Max-axis.Min))
		}
		if value < 0 {
			return int32(-value * float64(axis.Min))
		}
		return int32(value * float64(axis.Max))
	}
	return int32(value)
}

func clampAxis(v float64) float64 {
	switch {
	case v < -1:
		return -1
	case v > 1:
		return 1
	default:
		return v
	}
}

func (s *Server) gamepadID(pads *Gamepads, requested *int) (ebiten.GamepadID, error) {
	if requested != nil {
		return ebiten.GamepadID(*requested), nil
	}

	connected := pads.Connected()
	switch len(connected) {
	case 0:
		return 0, fmt.Errorf("no virtual controller is plugged in; call this with " +
			`{"connect": {}} first`)
	case 1:
		return connected[0], nil
	default:
		return 0, fmt.Errorf("several virtual controllers are plugged in; say which with id")
	}
}

// profile turns the request into a device description, filling in the default
// Xbox 360 identity for anything not given.
func (p *gamepadProfileInput) profile() (GamepadProfile, error) {
	profile := DefaultGamepadProfile()

	if p.Name != "" {
		profile.Name = p.Name
	}

	for _, field := range []struct {
		name  string
		value string
		into  *uint16
	}{
		{"vendor", p.Vendor, &profile.Vendor},
		{"product", p.Product, &profile.Product},
		{"version", p.Version, &profile.Version},
		{"bus", p.Bus, &profile.Bus},
	} {
		if field.value == "" {
			continue
		}
		n, err := strconv.ParseUint(field.value, 0, 16)
		if err != nil {
			return profile, fmt.Errorf("%s %q is not a number; write it as 0x045e or 1118",
				field.name, field.value)
		}
		*field.into = uint16(n)
	}

	return profile, nil
}

func parseCode(s string) (uint16, error) {
	n, err := strconv.ParseUint(s, 0, 16)
	if err != nil {
		return 0, fmt.Errorf("not an evdev code; write it as 0x130 or 304")
	}
	return uint16(n), nil
}

func names[T any](m map[string]T) string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func intPtr(v int) *int { return &v }
