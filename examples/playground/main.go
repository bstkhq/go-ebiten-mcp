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
	if err := ebitenmcp.RunGame(game,
		ebitenmcp.WithName("playground"),
		ebitenmcp.WithFactory(func() ebiten.Game { return NewGame() }),
	); err != nil {
		log.Fatal(err)
	}

	fmt.Println("playground: bye")
}
