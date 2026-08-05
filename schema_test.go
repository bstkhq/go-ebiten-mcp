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
