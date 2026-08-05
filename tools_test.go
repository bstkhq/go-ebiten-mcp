package ebitenmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools, tested by calling them.
//
// Until this file existed not one of them was reached by any test, for a
// structural reason rather than an oversight: Serve only runs when
// EBITEN_MCP_ADDR is set, the harness runs with it empty, so no *Server was ever
// built and every handler was unreachable. That is most of the package. A tool
// registered with a broken schema, or one that stopped answering, would have
// been found by a person with a client in front of them.
//
// None of this needs a socket. newServer assembles everything a handler uses and
// the handlers are called directly, against the same live game the rest of the
// file drives.

// untypedOutput was the list of tools still answering with a map, and it is
// empty.
//
// It survives as an empty map on purpose, with the test that reads it, so that
// adding a tool without an output schema fails rather than passes. The reason
// given for the last four — that their shape depends on what they found — did
// not hold up when it was looked at: what varies is how many gamepads there are,
// not which fields exist, and a slice is the answer to the first.
var untypedOutput = map[string]bool{}

func newTestServer(t *testing.T) *Server {
	t.Helper()

	s, err := newServer(testRT, &Options{Name: "probe"}, t.TempDir(), "http://127.0.0.1:0")
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}
	return s
}

func testContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithTimeout(context.Background(), 20*time.Second)
}

// TestEveryToolIsWellFormed is the cheapest guard in the package and covers all
// of them at once.
//
// A tool is a name, a description and a schema, and the SDK only checks the
// schema when a call arrives — so a tool whose input type does not describe an
// object reaches a client as a tool that cannot be called, with an error that
// blames the client. Registration happens here instead.
func TestEveryToolIsWellFormed(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	got := listTools(t, ctx, s)

	// Every tool this package documents having. The list is spelled out rather
	// than counted so that removing one is a decision somebody makes on purpose.
	want := []string{
		"game_screenshot", "game_record", "game_compare",
		"game_pause", "game_resume", "game_step", "game_set_tps", "game_wait", "game_reset",
		"game_state", "game_inspect", "game_traces", "game_frametimes", "game_goroutines",
		"game_key", "game_type", "game_mouse", "game_touch", "game_input_state",
		"game_gamepad", "game_profile", "game_script", "game_frames",
	}

	for _, name := range want {
		tool, ok := got[name]
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if len(tool.Description) < 40 {
			t.Errorf("%s has a description of %d characters; it is the only thing telling an agent when to reach for it",
				name, len(tool.Description))
		}
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema", name)
		}
		if tool.Annotations == nil || tool.Annotations.Title == "" {
			t.Errorf("%s has no title annotation", name)
		}

		// An output schema is what makes a result something a client can check
		// rather than a map it has to guess at, and the SDK derives it from the
		// handler's return type — so a tool without one is a tool still
		// answering with map[string]any. The list below is what is left; it may
		// shrink and must not grow.
		if tool.OutputSchema == nil && !untypedOutput[name] {
			t.Errorf("%s has no output schema: its handler still returns an untyped map", name)
		}
		if tool.OutputSchema != nil && untypedOutput[name] {
			t.Errorf("%s has an output schema now — take it off the untyped list", name)
		}
	}

	for name := range got {
		if !contains(want, name) {
			t.Errorf("%s is registered but not in the list this test knows about — add it, or take it out", name)
		}
	}
}

// TestStageIsDescribedTheSameEverywhere.
//
// A jsonschema tag has to be a string literal, so the four tools that take a
// stage each carry their own copy of the paragraph describing it. That is forced
// by Go; the copies drifting apart is not, and it is what went wrong — one of
// them once stated the opposite default. Comparing them here means a copy edited
// alone fails a test rather than reaching an agent as a contradiction.
func TestStageIsDescribedTheSameEverywhere(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tools := listTools(t, ctx, s)

	const shared = "which drawing step to read"
	descriptions := map[string][]string{}

	for name, tool := range tools {
		schema, ok := jsonObject(tool.InputSchema)
		if !ok {
			continue
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		stage, ok := properties["stage"].(map[string]any)
		if !ok {
			continue
		}

		text, _ := stage["description"].(string)
		if !strings.HasPrefix(text, shared) {
			t.Errorf("%s describes stage as %q, which does not start like the others", name, text)
			continue
		}
		descriptions[text] = append(descriptions[text], name)
	}

	if len(descriptions) == 0 {
		t.Fatal("no tool takes a stage, which cannot be right")
	}

	// game_frames is allowed to differ after the shared opening, because its
	// default really is the other one and saying so is the point. Everything
	// else has to match word for word.
	delete(descriptions, framesStageDescription(t, tools))

	if len(descriptions) > 1 {
		for text, names := range descriptions {
			t.Errorf("%v describe stage as:\n  %s", names, text)
		}
		t.Error("the copies of the stage description have drifted apart")
	}
}

func framesStageDescription(t *testing.T, tools map[string]*mcpsdk.Tool) string {
	t.Helper()

	schema, _ := jsonObject(tools["game_frames"].InputSchema)
	properties, _ := schema["properties"].(map[string]any)
	enable, _ := properties["enable"].(map[string]any)

	inner, ok := enable["properties"].(map[string]any)
	if !ok {
		return ""
	}
	stage, _ := inner["stage"].(map[string]any)
	text, _ := stage["description"].(string)

	return text
}

// TestScreenshotToolReturnsBothStages calls the tool rather than the runtime, so
// it also covers what a client actually receives: an image, a description of it,
// and the stage it came from.
func TestScreenshotToolReturnsBothStages(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	for _, stage := range []string{"final", "offscreen"} {
		result, out, err := s.screenshot(ctx, nil, screenshotInput{Stage: stage})
		if err != nil {
			t.Fatalf("game_screenshot at %s: %v", stage, err)
		}

		if string(out.Stage) != stage {
			t.Errorf("asked for %s and the result says %v", stage, out.Stage)
		}
		if out.Artifact == nil || out.Inline.Width == 0 {
			t.Errorf("the result describes no file or no inline size: %+v", out)
		}

		image, text := splitContent(t, result)
		if len(image.Data) == 0 {
			t.Errorf("%s came back with an empty image", stage)
		}
		if image.MIMEType != "image/png" {
			t.Errorf("%s came back as %q", stage, image.MIMEType)
		}
		if !strings.Contains(text, stage) {
			t.Errorf("the text for %s does not say which stage it is: %q", stage, text)
		}
	}
}

// TestStateToolAnswersWithoutTheLoop is the promise that matters when something
// has gone wrong: the tools that describe the process never touch the game loop,
// so they still answer when it has stopped.
func TestStateToolAnswersWithoutTheLoop(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	testRT.Pause()
	defer testRT.Resume()

	_, out, err := s.state(ctx, nil, emptyInput{})
	if err != nil {
		t.Fatalf("game_state on a paused game: %v", err)
	}

	if !out.Paused {
		t.Error("game_state says the game is not paused when it is")
	}
	if out.Loop != "paused" {
		t.Errorf("game_state calls the loop %q on a paused game", out.Loop)
	}
	if out.Tick == 0 || out.Go.Version == "" || out.MediaDir == "" {
		t.Errorf("game_state left something empty that always has a value: %+v", out)
	}
}

// TestInspectToolReadsUnexportedFields is the whole reason game_inspect exists:
// a Go game keeps almost everything unexported, and an inspector that respected
// visibility would show empty structs.
func TestInspectToolReadsUnexportedFields(t *testing.T) {
	game := reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	game.block(0) // touch it so the field is not optimised into nothing

	_, out, err := s.inspect(ctx, nil, inspectInput{Path: "fill"})
	if err != nil {
		t.Fatalf("game_inspect: %v", err)
	}
	if out.Value == nil {
		t.Errorf("game_inspect returned nothing for an unexported field")
	}
}

// TestInspectToolReturnsARegisteredSnapshot covers the half of game_inspect that
// a game has to opt into: a named view it publishes itself, computed rather than
// stored, for the questions no path can answer.
func TestInspectToolReturnsARegisteredSnapshot(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	testRT.RegisterState("probe", func(ebiten.Game) any {
		return map[string]any{"answer": 42}
	})
	defer testRT.UnregisterState("probe")

	_, out, err := s.inspect(ctx, nil, inspectInput{Path: "@probe"})
	if err != nil {
		t.Fatalf("game_inspect on a registered snapshot: %v", err)
	}

	value, ok := out.Value.(map[string]any)
	if !ok {
		t.Fatalf("the snapshot came back as %#v", out.Value)
	}
	if value["answer"] != int64(42) && value["answer"] != 42 {
		t.Errorf("the snapshot holds %v, want 42", value["answer"])
	}

	// And game_state lists what is available, or nobody would know to ask.
	_, state, err := s.state(ctx, nil, emptyInput{})
	if err != nil {
		t.Fatalf("game_state: %v", err)
	}
	if !contains(state.StateProviders, "probe") {
		t.Errorf("game_state does not list the registered snapshot: %v", state.StateProviders)
	}
}

// TestInspectToolSurvivesAPathThatPanics covers the read-only tool that could
// kill the game. Reflection over a value taken from an unexported field panics
// on Interface(), and before Do caught that, the panic unwound through the loop
// and took the process with it.
func TestInspectToolSurvivesAPathThatPanics(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// A path that does not exist is the polite version; either way the tool has
	// to answer rather than end the process.
	if _, _, err := s.inspect(ctx, nil, inspectInput{Path: "no.such.path"}); err == nil {
		t.Error("game_inspect accepted a path that does not exist")
	}

	if err := testRT.WaitTicks(ctx, 3); err != nil {
		t.Errorf("the loop stopped after an inspect that failed: %v", err)
	}
}

// TestLoopToolsHoldAndReleaseTheGame covers pause, step and resume through the
// tools, which is how anything deterministic gets done.
func TestLoopToolsHoldAndReleaseTheGame(t *testing.T) {
	game := reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if _, _, err := s.pause(ctx, nil, emptyInput{}); err != nil {
		t.Fatalf("game_pause: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	before := game.count()

	if _, _, err := s.step(ctx, nil, stepInput{Ticks: 5}); err != nil {
		t.Fatalf("game_step: %v", err)
	}
	if got := game.count() - before; got != 5 {
		t.Errorf("game_step ran %d updates, want 5", got)
	}

	if _, _, err := s.resume(ctx, nil, emptyInput{}); err != nil {
		t.Fatalf("game_resume: %v", err)
	}
	if err := testRT.WaitTicks(ctx, 3); err != nil {
		t.Errorf("the game did not run again after game_resume: %v", err)
	}
}

// TestStepToolBudgetsForTheTicksItWasAsked is the regression for a tool that
// failed on its own success. game_step used a flat five seconds while every
// sibling scaled with the work, so a step longer than about three hundred ticks
// always timed out — and then reported that the game loop was not running, when
// it had been stepping exactly as told.
func TestStepToolBudgetsForTheTicksItWasAsked(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	// Deliberately no deadline from here: the tool has to set its own, and the
	// point is that the one it sets fits the request.
	if _, _, err := s.step(context.Background(), nil, stepInput{Ticks: 400}); err != nil {
		t.Fatalf("game_step over 400 ticks: %v", err)
	}

	if _, _, err := s.resume(context.Background(), nil, emptyInput{}); err != nil {
		t.Fatalf("game_resume: %v", err)
	}
}

// TestKeyToolIsSeenByTheGame goes through the tool rather than the injector, so
// it covers the argument parsing an agent actually goes through.
func TestKeyToolIsSeenByTheGame(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if err := testRT.InputError(); err != nil {
		t.Skipf("input injection is unavailable here: %v", err)
	}

	_, out, err := s.key(ctx, nil, keyInput{Keys: []string{"arrowdown"}, Ticks: 2})
	if err != nil {
		t.Fatalf("game_key: %v", err)
	}
	if len(out.Keys) != 1 || out.Keys[0] != "arrowdown" {
		t.Errorf("game_key answered with keys=%v, want the one it was given", out.Keys)
	}
	if out.Tick == 0 {
		t.Error("game_key answered without saying which tick the press landed in")
	}

	if _, _, err := s.key(ctx, nil, keyInput{Keys: []string{"nonsense"}}); err == nil {
		t.Error("game_key accepted a key that does not exist")
	}
}

// TestTracesToolReturnsWhatTheProcessPrinted also covers the ring, which is
// filled from the descriptors and is the one part of this that works on a game
// nobody instrumented.
func TestTracesToolReturnsWhatTheProcessPrinted(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// The tap is not installed in tests, so the lines go in the way the pump
	// puts them there — already stripped, which is where that happens. What is
	// under test here is the tool and its filtering.
	s.traces.add("stdout", stripANSI("\x1b[31mhello from the game\x1b[0m"), testRT.Tick())
	s.traces.add("stderr", "something else entirely", testRT.Tick())

	_, out, err := s.tracesTool(ctx, nil, tracesInput{Contains: "hello"})
	if err != nil {
		t.Fatalf("game_traces: %v", err)
	}

	lines := out.Lines
	if len(lines) != 1 {
		t.Fatalf("filtering for 'hello' returned %d lines, want 1", len(lines))
	}
	if lines[0].Text != "hello from the game" {
		t.Errorf("the line came back as %q", lines[0].Text)
	}

	// And the stream filter is the other half of what makes this usable on a
	// game that prints a lot.
	_, out, err = s.tracesTool(ctx, nil, tracesInput{Stream: "stderr"})
	if err != nil {
		t.Fatalf("game_traces filtered by stream: %v", err)
	}
	for _, l := range out.Lines {
		if l.Stream != "stderr" {
			t.Errorf("filtering for stderr returned a %s line", l.Stream)
		}
	}
}

// TestFramesToolKeepsWhatCameBefore is the retrospective buffer, end to end
// through its tool: enable, let the game draw, ask for what happened.
func TestFramesToolKeepsWhatCameBefore(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if _, _, err := s.frames(ctx, nil, framesInput{Enable: &ringEnableInput{BudgetMB: 4}}); err != nil {
		t.Fatalf("enabling game_frames: %v", err)
	}
	defer s.frames(ctx, nil, framesInput{Disable: true})

	if err := testRT.WaitTicks(ctx, 30); err != nil {
		t.Fatalf("waiting for frames: %v", err)
	}

	result, out, err := s.frames(ctx, nil, framesInput{Last: 4})
	if err != nil {
		t.Fatalf("reading game_frames: %v", err)
	}

	kept := out.Frames
	if kept == 0 {
		t.Fatal("the buffer was enabled and kept nothing")
	}

	image, _ := splitContent(t, result)
	if len(image.Data) == 0 {
		t.Error("the contact sheet came back empty")
	}
}

// TestFramesToolRefusesToBeAskedBeforeItIsOn covers the error an agent is most
// likely to hit, and the one that has to say what to do about it.
func TestFramesToolRefusesToBeAskedBeforeItIsOn(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	s.frames(ctx, nil, framesInput{Disable: true})

	_, _, err := s.frames(ctx, nil, framesInput{Last: 4})
	if err == nil {
		t.Fatal("asking a buffer that is off returned frames")
	}
	if !strings.Contains(err.Error(), "enable") {
		t.Errorf("the error does not say how to turn it on: %v", err)
	}
}

// TestScriptToolRunsASequence covers the tool that exists to make a bug
// reproducible in one call.
func TestScriptToolRunsASequence(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if err := testRT.InputError(); err != nil {
		t.Skipf("input injection is unavailable here: %v", err)
	}

	_, out, err := s.script(ctx, nil, scriptInput{Steps: []scriptStep{
		{At: 0, Keys: []string{"arrowdown"}},
		{At: 4, Keys: []string{"enter"}},
		{At: 8, Screenshot: true, Label: "after"},
	}})
	if err != nil {
		t.Fatalf("game_script: %v", err)
	}

	if out.Steps != 3 {
		t.Errorf("the script reported %v steps, want 3", out.Steps)
	}
	if out.Captured != 1 {
		t.Errorf("the script captured %v frames, want the one it was asked for", out.Captured)
	}
}

// TestScriptToolStopsAtTheStepThatFailed: a sequence that carried on past a step
// that did not happen would produce a result meaning nothing, and hide which
// step was the problem.
func TestScriptToolStopsAtTheStepThatFailed(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if err := testRT.InputError(); err != nil {
		t.Skipf("input injection is unavailable here: %v", err)
	}

	_, _, err := s.script(ctx, nil, scriptInput{Steps: []scriptStep{
		{At: 0, Keys: []string{"arrowdown"}},
		{At: 2, Keys: []string{"not-a-key"}},
		{At: 4, Keys: []string{"enter"}},
	}})
	if err == nil {
		t.Fatal("a script with an impossible step reported success")
	}
	if !strings.Contains(err.Error(), "step 1") {
		t.Errorf("the error does not say which step failed: %v", err)
	}
}

// TestScriptToolReleasesWhatItPressedWhenItFails.
//
// The comment above ReleaseAll said a script that left a key held would poison
// whatever ran next and the failure would look like it belonged there — and then
// the release only happened when every step succeeded, so exactly that was true
// of any script that failed after pressing something.
func TestScriptToolReleasesWhatItPressedWhenItFails(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if err := testRT.InputError(); err != nil {
		t.Skipf("input injection is unavailable here: %v", err)
	}

	// Holds a key, then asks for one that does not exist.
	_, _, err := s.script(ctx, nil, scriptInput{Steps: []scriptStep{
		{At: 0, Keys: []string{"arrowdown"}, Hold: 30},
		{At: 1, Keys: []string{"not-a-key"}},
	}})
	if err == nil {
		t.Fatal("a script with an impossible step reported success")
	}

	if err := testRT.WaitTicks(ctx, 2); err != nil {
		t.Fatalf("waiting: %v", err)
	}

	var stillDown bool
	if err := testRT.Do(ctx, func() {
		stillDown = ebiten.IsKeyPressed(ebiten.KeyArrowDown)
	}); err != nil {
		t.Fatalf("reading the key: %v", err)
	}

	if stillDown {
		t.Error("the script failed with arrowdown still held, which the next tool would inherit")
	}
}

// TestFrametimesToolSplitsUpdateFromDraw. Knowing a tick is slow is half an
// answer; knowing which half it went into is the other.
func TestFrametimesToolSplitsUpdateFromDraw(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if err := testRT.WaitTicks(ctx, 10); err != nil {
		t.Fatalf("waiting for ticks: %v", err)
	}

	_, out, err := s.frametimes(ctx, nil, frametimesInput{Last: 10})
	if err != nil {
		t.Fatalf("game_frametimes: %v", err)
	}

	if out.Update.Mean == "" || out.Draw.Mean == "" || out.Wall.Mean == "" {
		t.Errorf("game_frametimes left a column empty: %+v", out)
	}
	if out.Ticks == 0 {
		t.Error("game_frametimes summarised no ticks")
	}
}

// TestCompareToolShowsWhatChanged, on a game whose picture is not moving, must
// report that nothing did — which is the answer that makes a positive result
// mean something.
func TestCompareToolShowsWhatChanged(t *testing.T) {
	reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, out, err := s.compare(ctx, nil, compareInput{WaitTicks: 5, Stage: "offscreen"})
	if err != nil {
		t.Fatalf("game_compare: %v", err)
	}

	if out.ChangedPixels != 0 {
		t.Errorf("a still game compared as %v changed pixels", out.ChangedPixels)
	}
	if out.TotalPixels != 64*48 {
		t.Errorf("compared %v pixels, want the game's own 64x48", out.TotalPixels)
	}
}

// TestGoroutinesToolAnswersWhenTheLoopIsWedged is the one that has to work
// precisely when nothing else does.
func TestGoroutinesToolAnswersWhenTheLoopIsWedged(t *testing.T) {
	game := reset(t)
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	game.block(600 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	_, out, err := s.goroutines(ctx, nil, goroutinesInput{})
	if err != nil {
		t.Fatalf("game_goroutines on a blocked loop: %v", err)
	}
	if out.Count == 0 {
		t.Error("game_goroutines counted none")
	}

	time.Sleep(700 * time.Millisecond)
	reset(t)
}

// splitContent pulls the image and the text out of a tool result, which is the
// shape every capturing tool returns.
func splitContent(t *testing.T, result *mcpsdk.CallToolResult) (*mcpsdk.ImageContent, string) {
	t.Helper()

	if result == nil {
		t.Fatal("the tool returned no content at all")
	}

	var (
		image *mcpsdk.ImageContent
		text  strings.Builder
	)
	for _, content := range result.Content {
		switch c := content.(type) {
		case *mcpsdk.ImageContent:
			image = c
		case *mcpsdk.TextContent:
			text.WriteString(c.Text)
		}
	}

	if image == nil {
		t.Fatalf("the tool returned no image: %#v", result.Content)
	}
	return image, text.String()
}

// listTools registers every tool the way a client sees them: over a real MCP
// session, so the schemas are the ones that would reach one.
func listTools(t *testing.T, ctx context.Context, s *Server) map[string]*mcpsdk.Tool {
	t.Helper()

	client, server := mcpsdk.NewInMemoryTransports()

	serverSession, err := s.mcp().Connect(ctx, server, nil)
	if err != nil {
		t.Fatalf("connecting the server: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil).
		Connect(ctx, client, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}

	tools := map[string]*mcpsdk.Tool{}
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func jsonObject(v any) (map[string]any, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}

	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return nil, false
	}
	return m, true
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
