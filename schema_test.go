package ebitenmcp

import (
	"encoding/json"
	"testing"
)

// The schema is only worth having if it says something. This checks one in
// detail rather than trusting that "not nil" means "useful".
func TestOutputSchemaDescribesTheAnswer(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tool := listTools(t, ctx, s)["game_key"]
	if tool == nil || tool.OutputSchema == nil {
		t.Fatal("game_key has no output schema")
	}

	schema, ok := jsonObject(tool.OutputSchema)
	if !ok {
		t.Fatalf("the schema does not marshal: %#v", tool.OutputSchema)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the schema has no properties: %s", mustJSON(t, schema))
	}
	for _, want := range []string{"tick", "keys", "held"} {
		if _, ok := props[want]; !ok {
			t.Errorf("the schema does not mention %q: %s", want, mustJSON(t, props))
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return string(data)
}

// TestTheExportedNamesSayWhatTheyAre.
//
// Four constants are aliases of internal/wire, so that the game and the
// launcher cannot disagree about a variable name. The cost is that godoc shows
// `const AddrEnv = wire.AddrEnv` and never the string, which leaves a reader
// with nothing to set. The documentation spells the value out instead, and this
// is what stops the two drifting apart.
func TestTheExportedNamesSayWhatTheyAre(t *testing.T) {
	for _, c := range []struct{ name, got, want string }{
		{"AddrEnv", AddrEnv, "EBITEN_MCP_ADDR"},
		{"CaptureEnv", CaptureEnv, "EBITEN_MCP_CAPTURE"},
		{"RendererEnv", RendererEnv, "EBITENMCP_RENDERER"},
		{"Path", Path, "/mcp"},
	} {
		if c.got != c.want {
			t.Errorf("%s is %q; the documentation says %q", c.name, c.got, c.want)
		}
	}
}
