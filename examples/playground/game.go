package main

import (
	"fmt"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	screenWidth  = 480
	screenHeight = 320

	headerHeight = 22
)

type screenID int

const (
	screenMenu screenID = iota
	screenPlayer
	screenPaint
	screenTouch
	screenForm
	screenStress
	screenCount
)

var screenNames = [screenCount]string{"menu", "player", "paint", "touch", "form", "stress"}

var (
	colBackground = color.RGBA{R: 0x14, G: 0x17, B: 0x1f, A: 0xff}
	colHeader     = color.RGBA{R: 0x22, G: 0x27, B: 0x35, A: 0xff}
	colAccent     = color.RGBA{R: 0x4c, G: 0xc2, B: 0xff, A: 0xff}
	colHot        = color.RGBA{R: 0xff, G: 0x6b, B: 0x4c, A: 0xff}
	colDim        = color.RGBA{R: 0x55, G: 0x5d, B: 0x70, A: 0xff}
)

// screen is one page of the playground. Each exists to give a particular tool
// something to act on and something visible to check afterwards.
type screen interface {
	update(g *Game) error
	draw(dst *ebiten.Image)
}

// Game is deliberately a tree with unexported fields at several depths, so that
// inspecting it by path (screens.player.pos.x) is a real exercise rather than
// reading one flat struct. A debugger that could only see exported fields would
// be useless on a Go game, and this is where that gets tested.
type Game struct {
	current screenID
	ticks   int

	screens struct {
		menu   *menuScreen
		player *playerScreen
		paint  *paintScreen
		touch  *touchScreen
		form   *formScreen
		stress *stressScreen
	}
}

// NewGame builds a playground in its initial state. It doubles as the factory
// handed to ebitenmcp, so game_reset and the test driver get a clean one.
func NewGame() *Game {
	g := &Game{}

	g.screens.menu = newMenuScreen()
	g.screens.player = newPlayerScreen()
	g.screens.paint = newPaintScreen()
	g.screens.touch = newTouchScreen()
	g.screens.form = newFormScreen()
	g.screens.stress = newStressScreen()

	return g
}

func (g *Game) Update() error {
	g.ticks++

	// Every sixtieth tick, a line on stdout. Nothing reads it here: it is there
	// so the trace ring, which captures the process's own output rather than
	// asking the game to log through a library, has something to capture.
	if g.ticks%60 == 0 {
		fmt.Printf("playground: tick %d on the %s screen\n", g.ticks, screenNames[g.current])
	}

	for i := screenMenu; i < screenCount; i++ {
		if inpututil.IsKeyJustPressed(ebiten.Key1 + ebiten.Key(i)) {
			g.current = i
		}
	}

	return g.screen().update(g)
}

func (g *Game) Draw(dst *ebiten.Image) {
	dst.Fill(colBackground)

	g.drawHeader(dst)

	g.screen().draw(dst)
}

func (g *Game) Layout(int, int) (int, int) { return screenWidth, screenHeight }

func (g *Game) screen() screen {
	switch g.current {
	case screenPlayer:
		return g.screens.player
	case screenPaint:
		return g.screens.paint
	case screenTouch:
		return g.screens.touch
	case screenForm:
		return g.screens.form
	case screenStress:
		return g.screens.stress
	default:
		return g.screens.menu
	}
}

// drawHeader puts the tab strip and the tick counter on screen. The tick is
// there so a captured frame carries the moment it was taken, which is what
// makes a contact sheet readable.
func (g *Game) drawHeader(dst *ebiten.Image) {
	vector.DrawFilledRect(dst, 0, 0, screenWidth, headerHeight, colHeader, false)

	x := float32(6)
	for i := screenMenu; i < screenCount; i++ {
		label := fmt.Sprintf("%d %s", i+1, screenNames[i])
		w := float32(len(label)*6 + 8)

		if i == g.current {
			vector.DrawFilledRect(dst, x, 3, w, headerHeight-6, colAccent, false)
		}
		ebitenutil.DebugPrintAt(dst, label, int(x)+4, 7)

		x += w + 2
	}

	ebitenutil.DebugPrintAt(dst, fmt.Sprintf("tick %d", g.ticks), screenWidth-64, 7)
}
