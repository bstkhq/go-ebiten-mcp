package main

import (
	"github.com/hajimehoshi/ebiten/v2"
)

// The final-screen pass, which exists here because it is the one thing a
// capture can silently get wrong.
//
// Ebitengine draws twice: Draw fills an offscreen at the logical resolution, and
// then DrawFinalScreen composites that onto the real screen. Anything a game
// does in the second step — a CRT filter, scanlines, a letterbox, a custom
// scaling filter — is invisible in the offscreen. A tool that captured the
// offscreen and called it a screenshot would show a clean image of a game that
// looks nothing like it.
//
// So the playground has one, and it is off until you press F, which is what
// makes the difference between the two stages something you can see rather than
// something to take on trust:
//
//	game_screenshot {"stage": "final"}      the scanlines
//	game_screenshot {"stage": "offscreen"}  the same frame without them
const crtSource = `//kage:unit pixels

package main

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	// One darker row out of every three real pixels — real pixels, not logical
	// ones, which is why this cannot be faked in the offscreen.
	line := 1.0 - step(1.0, mod(dstPos.y, 3.0))

	// Premultiplied: a faint green wash over everything, and a much darker band
	// on the scanline itself.
	alpha := 0.18 + 0.5*line
	return vec4(0.0, 0.10*(1.0-line), 0.0, alpha)
}
`

// DrawFinalScreen composites the offscreen onto the real screen, with the CRT
// pass over it when it is switched on.
//
// The default is Ebitengine's own rather than a hand-rolled blit: the real one
// chooses its filter from whether the scale is a whole number, and reproducing
// it by eye is how a game ends up looking subtly different under the debugger
// than it does when it runs.
func (g *Game) DrawFinalScreen(screen ebiten.FinalScreen, offscreen *ebiten.Image, geoM ebiten.GeoM) {
	ebiten.DefaultDrawFinalScreen(screen, offscreen, geoM)

	if !g.crt {
		return
	}

	if g.crtShader == nil {
		shader, err := ebiten.NewShader([]byte(crtSource))
		if err != nil {
			// A broken shader must not take the game down: say so once by
			// turning the effect off, and carry on drawing.
			g.crt = false
			return
		}
		g.crtShader = shader
	}

	bounds := screen.Bounds()
	screen.DrawRectShader(bounds.Dx(), bounds.Dy(), g.crtShader, &ebiten.DrawRectShaderOptions{})
}
