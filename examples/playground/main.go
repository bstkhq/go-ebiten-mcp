// Command playground is the test bed for go-ebiten-mcp.
//
// A trivial example proves nothing. This one exists so that every tool has
// something to act on and something visible to check afterwards: each screen is
// there to stress one part of the system, including the parts that only show up
// when things go wrong.
//
//	1 Menu    keyboard edges: a list that must move exactly one row per press
//	2 Player  held keys and continuous motion, for video and contact sheets
//	3 Paint   mouse move, drag and wheel, leaving a trail you can verify later
//	4 Touch   several simultaneous touches with their ids
//	5 Form    typed runes, which reach the game by a different path than keys
//	6 Stress  a framerate you can sink, a panic button and a blocking update
//	7 Pad     a virtual gamepad's buttons and axes, as the game sees them
//
// F toggles a CRT pass drawn in DrawFinalScreen, which is the one thing that
// exists in what the player sees and not in what the game's own Draw produced.
//
// It draws everything from code, so there are no assets and no dependencies
// beyond Ebitengine itself.
//
// Run it with the server on:
//
//	EBITEN_MCP_ADDR=127.0.0.1:8384 go run ./examples/playground
package main

import (
	"fmt"
	"log"

	ebitenmcp "github.com/bstkhq/go-ebiten-mcp"
	"github.com/hajimehoshi/ebiten/v2"
)

func main() {
	ebiten.SetWindowSize(screenWidth*2, screenHeight*2)
	ebiten.SetWindowTitle("go-ebiten-mcp playground")

	game := NewGame()

	// The one line. Without EBITEN_MCP_ADDR this is ebiten.RunGame and nothing
	// else: no socket, no goroutines.
	// A named view of the game, published for game_inspect to fetch as
	// "@summary". Walking the tree by path answers "what is this field"; this
	// answers "what is going on", which is a different question and one only the
	// game can answer — the screen's name and where the player is are computed
	// here and stored nowhere.
	//
	// It is handed the game that is running rather than closing over this one,
	// so it keeps telling the truth after game_reset builds a new one.
	summary := ebitenmcp.WithState("summary", func(current ebiten.Game) any {
		g, ok := current.(*Game)
		if !ok {
			return nil
		}
		return map[string]any{
			"screen": screenNames[g.current],
			"tick":   g.ticks,
			"crt":    g.crt,
			"player": map[string]float64{"x": g.screens.player.pos.x, "y": g.screens.player.pos.y},
		}
	})

	if err := ebitenmcp.RunGame(game,
		ebitenmcp.WithName("playground"),
		ebitenmcp.WithFactory(func() ebiten.Game { return NewGame() }),
		summary,
	); err != nil {
		log.Fatal(err)
	}

	fmt.Println("playground: bye")
}
