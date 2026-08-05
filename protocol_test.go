package ebitenmcp

import (
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools called the way a client calls them, over a session rather than by
// reaching for the handler.
//
// There is a whole layer between the two, and the rest of the suite is blind to
// it: the SDK marshals the typed answer, validates it against the output schema
// the tool declared, and fills the text content from the result. Both defects
// found by hand yesterday lived in exactly that gap — a field erased by
// omitempty because it carried the caller's word for something instead of the
// answer, and four arrays that came back as null because a nil slice is not an
// empty one. Neither was reachable by calling the handler, and both were
// obvious the first time somebody used the tool.
//
// So this calls all twenty-three, once, with arguments chosen to cost the suite
// as little as possible. It is a sweep, not a study of any one of them: what it
// asserts is that the tool answers, that the SDK accepts the answer against the
// schema, and that nothing the schema calls an array arrives as null.

// protocolCall is one tool, arguments that make it do its work cheaply, and
// what the machine has to have for it to run at all.
type protocolCall struct {
	name  string
	args  map[string]any
	needs string // "input", "gamepad", or nothing
}

// Every tool, and why each one is called the way it is. The list is spelled out
// rather than derived so that a tool added without a line here fails the check
// at the bottom instead of quietly never being called.
var protocolCalls = []protocolCall{
	{name: "game_screenshot", args: map[string]any{"stage": "offscreen"}},

	// Four frames and no video: the point is that the tool answers, and
	// encoding sixty frames through ffmpeg to learn that would be a minute of
	// suite time for nothing. video_test.go covers the encoder itself.
	{name: "game_record", args: map[string]any{"frames": 4, "no_video": true}},
	{name: "game_compare", args: map[string]any{"wait_ticks": 2, "stage": "offscreen"}},

	{name: "game_pause", args: map[string]any{}},
	{name: "game_step", args: map[string]any{"ticks": 1}},
	{name: "game_resume", args: map[string]any{}},
	{name: "game_set_tps", args: map[string]any{"tps": 60}},
	{name: "game_wait", args: map[string]any{"ticks": 1}},
	{name: "game_reset", args: map[string]any{}},

	{name: "game_state", args: map[string]any{}},
	{name: "game_inspect", args: map[string]any{"path": "fill"}},
	{name: "game_traces", args: map[string]any{}},
	{name: "game_frametimes", args: map[string]any{"last": 5}},
	{name: "game_goroutines", args: map[string]any{}},

	{name: "game_key", args: map[string]any{"keys": []any{"a"}}, needs: "input"},
	{name: "game_type", args: map[string]any{"text": "x"}, needs: "input"},
	{name: "game_mouse", args: map[string]any{"x": 10, "y": 10}, needs: "input"},
	{name: "game_touch", args: map[string]any{
		"touches": []any{map[string]any{"id": 1, "x": 5, "y": 5}},
	}, needs: "input"},

	// No injection needed: reading what is pressed is not pressing anything,
	// and on a build with injection compiled out this is still the tool that
	// answers "nothing is".
	{name: "game_input_state", args: map[string]any{}},

	{name: "game_gamepad", args: map[string]any{"connect": map[string]any{}}, needs: "gamepad"},

	// heap rather than cpu: cpu samples for five seconds of wall clock and takes
	// the process-global profiler while it does, which would collide with
	// anything else profiling this binary — `go test -cpuprofile` included.
	{name: "game_profile", args: map[string]any{"kind": "heap", "lines": 5}},

	{name: "game_script", args: map[string]any{
		"steps": []any{map[string]any{"at": 0, "keys": []any{"a"}}},
	}, needs: "input"},

	// Turning the buffer on is the cheapest of the three things this tool does,
	// and the only one that needs nothing to have happened first.
	{name: "game_frames", args: map[string]any{
		"enable": map[string]any{"budget_mb": 1, "every": 30},
	}},
}

// TestEveryToolAnswersOverTheProtocol.
//
// The suite's blind spot, in one test. A handler returning a struct the SDK
// then refuses to marshal against its own declared schema is a tool that works
// in every test here and fails the first time a client calls it, with an error
// naming neither the tool nor the field.
func TestEveryToolAnswersOverTheProtocol(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// A sweep that calls everything changes everything, and the tools it leaves
	// behind are the ones the next test inherits. game_set_tps sets
	// process-global state on the one loop the whole package shares; game_reset
	// swaps the game; game_gamepad plugs in a controller and there is no line in
	// the table for unplugging it — which is worth spelling out, because two
	// controllers plugged in makes every later call that does not name one
	// ambiguous, and the tests after this one then fail looking like a gamepad
	// bug. Registered up here so it happens even if the sweep gives up halfway.
	tps := ebiten.TPS()
	t.Cleanup(func() {
		ebiten.SetTPS(tps)
		testRT.Resume()

		pads := testRT.Gamepads()
		for _, id := range pads.Connected() {
			pads.Disconnect(id)
		}
	})

	tools := listTools(t, ctx, s)
	session := newTestSession(t, ctx, s)

	for _, c := range protocolCalls {
		t.Run(c.name, func(t *testing.T) {
			switch c.needs {
			case "input":
				if err := testRT.InputError(); err != nil {
					t.Skipf("input injection is unavailable here: %v", err)
				}
			case "gamepad":
				requireGamepads(t)
			}

			result := callTool(t, ctx, session, c.name, c.args)
			if result.IsError {
				_, text := textOf(result)
				t.Fatalf("the tool answered with an error: %s", text)
			}

			// A tool with an output schema must produce structured content: the
			// SDK derives one from the other, so a nil here means the answer
			// never made it through the layer this test exists to cross.
			tool := tools[c.name]
			if tool == nil {
				t.Fatalf("%s is not registered", c.name)
			}
			if tool.OutputSchema != nil && result.StructuredContent == nil {
				t.Fatal("it declares an output schema and returned no structured content")
			}

			for _, field := range nullArrays(t, tool, result.StructuredContent) {
				t.Errorf("%s came back as null; its schema says array, and a client "+
					"that trusts the schema and loops over it gets nothing", field)
			}
		})
	}

	// A tool nobody calls here is a tool back in the blind spot.
	called := map[string]bool{}
	for _, c := range protocolCalls {
		called[c.name] = true
	}
	for name := range tools {
		if !called[name] {
			t.Errorf("%s is registered but this sweep never calls it — add a line for it", name)
		}
	}

	// The sweep pressed keys, put fingers down and swapped the game. Leave the
	// loop as it was found.
	testRT.Injector().ReleaseAll()
	reset(t)
}

// nullArrays returns the fields the tool's schema declares as arrays and whose
// answer came back as null.
//
// Absent is fine and is not the same thing: omitempty leaves out a field that
// has nothing to say, and a client checking for a key it did not get behaves.
// A key that is present and null is what breaks it, because the schema promised
// something to iterate.
//
// Deliberately stricter than the schema, and it has to be. The SDK derives
// "type": ["null", "array"] for every Go slice, since a nil one is legal JSON
// null — so the schema permits exactly the answer this is looking for, and the
// SDK's own validation will never object. That permission is why the defect
// reached a client in the first place. These tools mean an array.
func nullArrays(t *testing.T, tool *mcpsdk.Tool, content any) []string {
	t.Helper()

	if tool == nil || tool.OutputSchema == nil || content == nil {
		return nil
	}

	schema, ok := jsonObject(tool.OutputSchema)
	if !ok {
		t.Fatalf("the output schema does not marshal: %#v", tool.OutputSchema)
	}
	props, _ := schema["properties"].(map[string]any)

	answer, ok := jsonObject(content)
	if !ok {
		t.Fatalf("the structured content does not marshal: %#v", content)
	}

	var nulls []string
	for name, raw := range props {
		field, ok := raw.(map[string]any)
		if !ok || !isArray(field["type"]) {
			continue
		}

		value, present := answer[name]
		if present && value == nil {
			nulls = append(nulls, name)
		}
	}
	return nulls
}

// isArray reads a schema's type, which is a string when there is one and a list
// when there are several. Reading only the first shape is how the check above
// spent its first run matching nothing at all and passing everything.
func isArray(v any) bool {
	switch t := v.(type) {
	case string:
		return t == "array"
	case []any:
		for _, one := range t {
			if one == "array" {
				return true
			}
		}
	}
	return false
}

// textOf is splitContent without the image, for the results that have none.
func textOf(result *mcpsdk.CallToolResult) (int, string) {
	var (
		images int
		text   string
	)
	for _, content := range result.Content {
		switch c := content.(type) {
		case *mcpsdk.ImageContent:
			images++
		case *mcpsdk.TextContent:
			text += c.Text
		}
	}
	return images, text
}
