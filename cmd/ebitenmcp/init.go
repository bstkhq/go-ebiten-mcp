package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// `ebitenmcp init` wires a game's repository up so an agent working in it finds
// the tools on its own.
//
// Two files, because they answer two different questions. .mcp.json is how the
// tools get into the agent's list at all. The note in CLAUDE.md is how it knows
// to reach for them instead of guessing from the source what the game looks
// like — a tool list says what is possible, not what is worth doing.

const claudeSection = `## Seeing the game run

This project is wired to go-ebiten-mcp, so the game can be looked at and driven
while it runs.

If the game is already running — which on a machine with a screen it usually is —
its tools are already there. If it is not, ` + "`game_start`" + ` runs it, bringing up a
display first on a machine that has none.

Reach for them instead of reasoning about the source when the question is what
the game actually does:

- ` + "`game_screenshot`" + ` to see the current frame, ` + "`game_record`" + ` for a grid of frames
  over time, which is how motion reads.
- ` + "`game_key`" + `, ` + "`game_mouse`" + `, ` + "`game_touch`" + ` and ` + "`game_type`" + ` to drive it. They take
  ` + "`then_wait_ticks`" + ` and ` + "`then_screenshot`" + `, so acting and looking is one call.
- ` + "`game_inspect`" + ` to read the game's own state by path, unexported fields
  included, when the pixels are not enough to say why.
- ` + "`game_state`" + `, ` + "`game_traces`" + ` and ` + "`game_frametimes`" + ` when it misbehaves. They keep
  answering after the game has panicked or deadlocked, and they say which.

Showing a change is a screenshot, not a description of one.
`

func initCommand(args []string) error {
	start := strings.Join(args, " ")
	if start == "" {
		guessed, err := guessGameCommand()
		if err != nil {
			return err
		}
		start = guessed
		fmt.Fprintf(os.Stderr, "ebitenmcp: using %q as the game; pass a command to init to change it\n", start)
	}

	if err := writeMCPConfig(start); err != nil {
		return err
	}
	return appendClaudeNote()
}

// guessGameCommand looks for the obvious main package rather than asking, since
// the obvious one is right nearly always and wrong visibly.
func guessGameCommand() (string, error) {
	for _, candidate := range []string{"./cmd/game", "./cmd/player", "."} {
		if _, err := os.Stat(strings.TrimPrefix(candidate, "./") + "/main.go"); err == nil {
			return "go run " + candidate, nil
		}
		if candidate == "." {
			if _, err := os.Stat("main.go"); err == nil {
				return "go run .", nil
			}
		}
	}

	entries, err := os.ReadDir("cmd")
	if err == nil && len(entries) > 0 {
		return "go run ./cmd/" + entries[0].Name(), nil
	}

	return "", fmt.Errorf("could not tell which package is the game; " +
		"pass it, for example: ebitenmcp init go run ./cmd/mygame")
}

func writeMCPConfig(start string) error {
	const path = ".mcp.json"

	config := map[string]any{"mcpServers": map[string]any{}}

	// Merge rather than replace: a project may already have servers configured,
	// and silently dropping them would be a nasty thing for a setup command to
	// do.
	if existing, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(existing, &config); err != nil {
			return fmt.Errorf("%s exists and is not valid JSON: %w", path, err)
		}
		if config["mcpServers"] == nil {
			config["mcpServers"] = map[string]any{}
		}
	}

	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		return fmt.Errorf("%s has an mcpServers that is not an object", path)
	}

	// Two entries, because they answer different questions. The game's own
	// server is the one that matters and the one used day to day: it is already
	// there whenever the game is running, which on a machine with a screen is
	// most of the time. The control server is for when it is not.
	servers["game"] = map[string]any{
		"type": "http",
		"url":  "http://" + defaultAddr + Path,
	}
	// `go run <module>@latest` rather than a bare `ebitenmcp`, which would need
	// this binary to be on the client's $PATH — and an MCP client started from a
	// desktop launcher often does not have the $PATH a shell does. That fails as
	// "command not found", which explains nothing and is nobody's fault. Go
	// fetches and caches the build once.
	servers["game-control"] = map[string]any{
		"command": "go",
		"args":    []string{"run", modulePath + "/cmd/ebitenmcp@latest", "mcp", "--start", start},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}

	fmt.Printf("wrote %s\n  game          the running game's own tools, at http://%s%s\n"+
		"  game-control  starts it when it is not running: %q\n", path, defaultAddr, Path, start)
	return nil
}

func appendClaudeNote() error {
	const path = "CLAUDE.md"

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(existing), "go-ebiten-mcp") {
		fmt.Printf("%s already mentions go-ebiten-mcp, leaving it alone\n", path)
		return nil
	}

	body := claudeSection
	if len(existing) > 0 {
		body = strings.TrimRight(string(existing), "\n") + "\n\n" + claudeSection
	}

	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}

	fmt.Printf("wrote %s: what the tools are for, so an agent reaches for them\n", path)
	return nil
}
