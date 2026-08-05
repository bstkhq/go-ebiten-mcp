package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// init writes into somebody else's repository, which is the reason to test it:
// the failure everybody remembers from a tool like this is the one that
// replaced a file it should have added to.

func TestInitLeavesWhatItFindsAlone(t *testing.T) {
	t.Chdir(t.TempDir())

	// A project that already has notes of its own and a client of its own.
	mustWrite(t, "CLAUDE.md", "# My game\n\nDo not lose this.\n")
	mustWrite(t, ".mcp.json", `{"mcpServers":{"other":{"command":"true"}}}`)

	if err := initCommand([]string{"go", "run", "./cmd/mygame"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	claude := mustRead(t, "CLAUDE.md")
	if !strings.Contains(claude, "Do not lose this.") {
		t.Error("it overwrote the project's own notes")
	}
	if !strings.Contains(claude, "go-ebiten-mcp") {
		t.Error("it did not say the tools are there")
	}

	var config struct {
		Servers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, ".mcp.json")), &config); err != nil {
		t.Fatalf("the config it wrote is not JSON: %v", err)
	}
	for _, want := range []string{"other", "game", "game-control"} {
		if _, ok := config.Servers[want]; !ok {
			t.Errorf("%s is missing from .mcp.json", want)
		}
	}
}

// TestInitTwiceChangesNothingTheSecondTime. Running it again is the normal way
// to pick up a new version, so it has to be safe — and a note appended twice is
// the obvious way for it not to be.
func TestInitTwiceChangesNothingTheSecondTime(t *testing.T) {
	t.Chdir(t.TempDir())

	for i := 0; i < 2; i++ {
		if err := initCommand([]string{"go", "run", "."}); err != nil {
			t.Fatalf("init %d: %v", i+1, err)
		}
	}

	if n := strings.Count(mustRead(t, "CLAUDE.md"), "## Seeing the game run"); n != 1 {
		t.Errorf("the note is in CLAUDE.md %d times", n)
	}
}

func mustWrite(t *testing.T, name, body string) {
	t.Helper()

	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(body)
}
