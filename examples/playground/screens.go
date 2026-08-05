package main

import (
	"fmt"
	"image/color"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// ---------------------------------------------------------------------------
// 1 menu — one row per press, and no more
// ---------------------------------------------------------------------------

// menuScreen is the sharpest test of the injection's timing. It moves on the
// edge, so a press that reported "just pressed" on two consecutive ticks would
// skip a row, and you would see it.
type menuScreen struct {
	items    []string
	selected int
	chosen   string
	moves    int
}

func newMenuScreen() *menuScreen {
	return &menuScreen{
		items: []string{"new game", "continue", "options", "credits", "quit"},
	}
}

func (s *menuScreen) update(*Game) error {
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowDown):
		s.selected = (s.selected + 1) % len(s.items)
		s.moves++
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowUp):
		s.selected = (s.selected - 1 + len(s.items)) % len(s.items)
		s.moves++
	case inpututil.IsKeyJustPressed(ebiten.KeyEnter):
		s.chosen = s.items[s.selected]
	}
	return nil
}

func (s *menuScreen) draw(dst *ebiten.Image) {
	for i, item := range s.items {
		y := headerHeight + 24 + i*22

		if i == s.selected {
			vector.DrawFilledRect(dst, 24, float32(y)-4, 200, 20, colAccent, false)
		}
		ebitenutil.DebugPrintAt(dst, item, 32, y)
	}

	ebitenutil.DebugPrintAt(dst, fmt.Sprintf("selected %d  moves %d", s.selected, s.moves), 24, screenHeight-40)
	if s.chosen != "" {
		ebitenutil.DebugPrintAt(dst, "chosen: "+s.chosen, 24, screenHeight-24)
	}
}

// ---------------------------------------------------------------------------
// 2 player — held keys and continuous motion
// ---------------------------------------------------------------------------

type point struct {
	x float64
	y float64
}

// playerScreen accelerates while a key is held rather than moving a fixed step,
// so the difference between "tapped" and "held for thirty ticks" is obvious in
// a single frame, and a recording of it has something to show.
type playerScreen struct {
	pos point
	vel point

	held int
}

func newPlayerScreen() *playerScreen {
	return &playerScreen{pos: point{x: screenWidth / 2, y: screenHeight / 2}}
}

func (s *playerScreen) update(*Game) error {
	const accel, friction, maxSpeed = 0.45, 0.88, 6.0

	if ebiten.IsKeyPressed(ebiten.KeyArrowLeft) {
		s.vel.x -= accel
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowRight) {
		s.vel.x += accel
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowUp) {
		s.vel.y -= accel
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowDown) {
		s.vel.y += accel
	}

	s.held = 0
	for _, k := range []ebiten.Key{ebiten.KeyArrowLeft, ebiten.KeyArrowRight, ebiten.KeyArrowUp, ebiten.KeyArrowDown} {
		if d := inpututil.KeyPressDuration(k); d > s.held {
			s.held = d
		}
	}

	s.vel.x = clamp(s.vel.x*friction, -maxSpeed, maxSpeed)
	s.vel.y = clamp(s.vel.y*friction, -maxSpeed, maxSpeed)

	s.pos.x = clamp(s.pos.x+s.vel.x, 12, screenWidth-12)
	s.pos.y = clamp(s.pos.y+s.vel.y, headerHeight+12, screenHeight-12)

	return nil
}

func (s *playerScreen) draw(dst *ebiten.Image) {
	// A trail in the direction of travel, so a still frame shows movement.
	for i := 1; i <= 6; i++ {
		f := float32(i)
		vector.DrawFilledCircle(dst,
			float32(s.pos.x-s.vel.x*float64(i)), float32(s.pos.y-s.vel.y*float64(i)),
			10-f, fade(colAccent, 1-float64(i)/7), true)
	}
	vector.DrawFilledCircle(dst, float32(s.pos.x), float32(s.pos.y), 10, colAccent, true)

	ebitenutil.DebugPrintAt(dst, fmt.Sprintf("pos %.1f,%.1f   vel %.2f,%.2f   held %d ticks",
		s.pos.x, s.pos.y, s.vel.x, s.vel.y, s.held), 8, screenHeight-20)
}

// ---------------------------------------------------------------------------
// 3 paint — a drag you can check after the fact
// ---------------------------------------------------------------------------

// paintScreen keeps what was drawn. A gesture that only showed while it was
// happening could not be verified from a screenshot taken afterwards, which is
// exactly the situation an agent is in.
type paintScreen struct {
	strokes []stroke
	width   float32
	drawing bool
}

type stroke struct {
	points []point
	width  float32
}

func newPaintScreen() *paintScreen {
	return &paintScreen{width: 4}
}

func (s *paintScreen) update(*Game) error {
	if _, dy := ebiten.Wheel(); dy != 0 {
		s.width = float32(clamp(float64(s.width)+dy, 1, 24))
	}

	x, y := ebiten.CursorPosition()
	inCanvas := y > headerHeight

	switch {
	case inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) && inCanvas:
		s.strokes = append(s.strokes, stroke{width: s.width})
		s.drawing = true
	case !ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft):
		s.drawing = false
	}

	if s.drawing && inCanvas {
		last := &s.strokes[len(s.strokes)-1]
		p := point{x: float64(x), y: float64(y)}

		if n := len(last.points); n == 0 || last.points[n-1] != p {
			last.points = append(last.points, p)
		}
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
		s.strokes = nil
	}

	return nil
}

func (s *paintScreen) draw(dst *ebiten.Image) {
	for _, st := range s.strokes {
		for i := 1; i < len(st.points); i++ {
			a, b := st.points[i-1], st.points[i]
			vector.StrokeLine(dst, float32(a.x), float32(a.y), float32(b.x), float32(b.y), st.width, colHot, true)
		}
		if len(st.points) == 1 {
			p := st.points[0]
			vector.DrawFilledCircle(dst, float32(p.x), float32(p.y), st.width/2, colHot, true)
		}
	}

	x, y := ebiten.CursorPosition()
	vector.StrokeRect(dst, float32(x)-6, float32(y)-6, 12, 12, 1, colDim, false)

	ebitenutil.DebugPrintAt(dst, fmt.Sprintf("drag to paint  wheel resizes  backspace clears\ncursor %d,%d  width %.0f  strokes %d",
		x, y, s.width, len(s.strokes)), 8, screenHeight-32)
}

// ---------------------------------------------------------------------------
// 4 touch — several fingers at once
// ---------------------------------------------------------------------------

type touchScreen struct {
	seen map[ebiten.TouchID]point
	max  int
}

func newTouchScreen() *touchScreen {
	return &touchScreen{seen: map[ebiten.TouchID]point{}}
}

func (s *touchScreen) update(*Game) error {
	ids := ebiten.AppendTouchIDs(nil)

	clear(s.seen)
	for _, id := range ids {
		x, y := ebiten.TouchPosition(id)
		s.seen[id] = point{x: float64(x), y: float64(y)}
	}

	if len(ids) > s.max {
		s.max = len(ids)
	}
	return nil
}

func (s *touchScreen) draw(dst *ebiten.Image) {
	for id, p := range s.seen {
		c := touchColor(int(id))

		vector.DrawFilledCircle(dst, float32(p.x), float32(p.y), 22, fade(c, 0.35), true)
		vector.StrokeCircle(dst, float32(p.x), float32(p.y), 22, 2, c, true)
		ebitenutil.DebugPrintAt(dst, fmt.Sprintf("#%d", id), int(p.x)-8, int(p.y)-4)
	}

	ebitenutil.DebugPrintAt(dst, fmt.Sprintf("touches now %d   most seen at once %d", len(s.seen), s.max), 8, screenHeight-20)
}

// ---------------------------------------------------------------------------
// 5 form — typed runes
// ---------------------------------------------------------------------------

// formScreen reads AppendInputChars, which is a different path through
// Ebitengine than the key arrays. Text arriving here proves the runes were
// injected, not that a key was.
type formScreen struct {
	text     string
	commits  []string
	blinkOn  bool
	blinkFor int
}

func newFormScreen() *formScreen {
	return &formScreen{}
}

func (s *formScreen) update(*Game) error {
	s.text += string(ebiten.AppendInputChars(nil))

	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) && s.text != "" {
		r := []rune(s.text)
		s.text = string(r[:len(r)-1])
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) && s.text != "" {
		s.commits = append(s.commits, s.text)
		s.text = ""
	}

	s.blinkFor++
	if s.blinkFor >= 30 {
		s.blinkFor, s.blinkOn = 0, !s.blinkOn
	}
	return nil
}

func (s *formScreen) draw(dst *ebiten.Image) {
	vector.StrokeRect(dst, 24, headerHeight+30, screenWidth-48, 26, 1, colDim, false)
	ebitenutil.DebugPrintAt(dst, s.text, 32, headerHeight+38)

	if s.blinkOn {
		x := float32(32 + len(s.text)*6)
		vector.DrawFilledRect(dst, x, headerHeight+36, 6, 12, colAccent, false)
	}

	ebitenutil.DebugPrintAt(dst, "type; enter commits; backspace deletes", 24, headerHeight+64)
	for i, c := range s.commits {
		ebitenutil.DebugPrintAt(dst, "> "+c, 32, headerHeight+88+i*16)
	}
}

// ---------------------------------------------------------------------------
// 6 stress — the screen for the things that go wrong
// ---------------------------------------------------------------------------

// stressScreen is the one that earns the playground its keep. Frame timings are
// only interesting when the framerate can be sunk on demand, and the crash and
// stall behaviour cannot be tested on a game that never misbehaves.
type stressScreen struct {
	count int

	panicNext bool
	blockFor  time.Duration
	blocked   int
}

func newStressScreen() *stressScreen {
	return &stressScreen{count: 0}
}

var stressButtons = []struct {
	label  string
	x, y   float32
	w, h   float32
	action string
}{
	{"-1000", 24, headerHeight + 24, 60, 22, "less"},
	{"+1000", 92, headerHeight + 24, 60, 22, "more"},
	{"panic", 24, headerHeight + 58, 60, 22, "panic"},
	{"block 3s", 92, headerHeight + 58, 74, 22, "block"},
}

func (s *stressScreen) update(*Game) error {
	if s.panicNext {
		s.panicNext = false
		panic("playground: the panic button was pressed")
	}

	if s.blockFor > 0 {
		d := s.blockFor
		s.blockFor = 0
		s.blocked++
		time.Sleep(d)
	}

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		x, y := ebiten.CursorPosition()
		for _, b := range stressButtons {
			if float32(x) < b.x || float32(x) > b.x+b.w || float32(y) < b.y || float32(y) > b.y+b.h {
				continue
			}
			s.act(b.action)
		}
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
		s.act("more")
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
		s.act("less")
	}

	return nil
}

func (s *stressScreen) act(action string) {
	switch action {
	case "more":
		s.count = min(s.count+1000, 20000)
	case "less":
		s.count = max(s.count-1000, 0)
	case "panic":
		s.panicNext = true
	case "block":
		s.blockFor = 3 * time.Second
	}
}

func (s *stressScreen) draw(dst *ebiten.Image) {
	for _, b := range stressButtons {
		vector.StrokeRect(dst, b.x, b.y, b.w, b.h, 1, colDim, false)
		ebitenutil.DebugPrintAt(dst, b.label, int(b.x)+6, int(b.y)+7)
	}

	// Each sprite is its own draw call, which is what makes the cost real
	// rather than something the GPU batches away.
	for i := 0; i < s.count; i++ {
		f := float64(i)
		x := float32(240 + 90*math.Cos(f*0.37))
		y := float32(190 + 70*math.Sin(f*0.53))
		vector.DrawFilledRect(dst, x, y, 3, 3, colHot, false)
	}

	ebitenutil.DebugPrintAt(dst, fmt.Sprintf("sprites %d   blocked %d times\nactual %.1f fps / %.1f tps",
		s.count, s.blocked, ebiten.ActualFPS(), ebiten.ActualTPS()), 24, screenHeight-32)
}

// ---------------------------------------------------------------------------

func clamp(v, lo, hi float64) float64 {
	return math.Min(math.Max(v, lo), hi)
}

func fade(c color.RGBA, alpha float64) color.RGBA {
	a := uint8(clamp(alpha, 0, 1) * 255)
	return color.RGBA{
		R: uint8(float64(c.R) * float64(a) / 255),
		G: uint8(float64(c.G) * float64(a) / 255),
		B: uint8(float64(c.B) * float64(a) / 255),
		A: a,
	}
}

func touchColor(id int) color.RGBA {
	palette := []color.RGBA{
		{R: 0x4c, G: 0xc2, B: 0xff, A: 0xff},
		{R: 0xff, G: 0x6b, B: 0x4c, A: 0xff},
		{R: 0x8b, G: 0xe0, B: 0x6a, A: 0xff},
		{R: 0xff, G: 0xd1, B: 0x4c, A: 0xff},
		{R: 0xc3, G: 0x8b, B: 0xff, A: 0xff},
	}
	return palette[((id%len(palette))+len(palette))%len(palette)]
}

// ---------------------------------------------------------------------------
// 7 pad — a controller that is not there
// ---------------------------------------------------------------------------

// gamepadScreen shows what the game sees of a controller, which is the only way
// to check at a glance that an injected press actually arrived. It reads the
// standard layout, because that is what a game written for "any pad" uses, and
// shows the SDL id, because that is what a game switches on to decide what kind
// of controller it is holding.
type gamepadScreen struct {
	connected []ebiten.GamepadID
	presses   int
}

func newGamepadScreen() *gamepadScreen { return &gamepadScreen{} }

var padButtons = []struct {
	name   string
	button ebiten.StandardGamepadButton
}{
	{"A", ebiten.StandardGamepadButtonRightBottom},
	{"B", ebiten.StandardGamepadButtonRightRight},
	{"X", ebiten.StandardGamepadButtonRightLeft},
	{"Y", ebiten.StandardGamepadButtonRightTop},
	{"LB", ebiten.StandardGamepadButtonFrontTopLeft},
	{"RB", ebiten.StandardGamepadButtonFrontTopRight},
	{"back", ebiten.StandardGamepadButtonCenterLeft},
	{"start", ebiten.StandardGamepadButtonCenterRight},
	{"up", ebiten.StandardGamepadButtonLeftTop},
	{"down", ebiten.StandardGamepadButtonLeftBottom},
	{"left", ebiten.StandardGamepadButtonLeftLeft},
	{"right", ebiten.StandardGamepadButtonLeftRight},
}

func (s *gamepadScreen) update(*Game) error {
	s.connected = ebiten.AppendGamepadIDs(s.connected[:0])

	for _, id := range s.connected {
		for _, b := range padButtons {
			if inpututil.IsStandardGamepadButtonJustPressed(id, b.button) {
				s.presses++
			}
		}
	}
	return nil
}

func (s *gamepadScreen) draw(dst *ebiten.Image) {
	if len(s.connected) == 0 {
		ebitenutil.DebugPrintAt(dst, "no gamepad connected", 24, headerHeight+30)
		return
	}

	id := s.connected[0]

	ebitenutil.DebugPrintAt(dst, fmt.Sprintf("%s\n%s   standard layout: %v   edges seen: %d",
		ebiten.GamepadName(id), ebiten.GamepadSDLID(id),
		ebiten.IsStandardGamepadLayoutAvailable(id), s.presses), 8, headerHeight+8)

	for i, b := range padButtons {
		x := float32(16 + (i%6)*76)
		y := float32(headerHeight + 46 + (i/6)*30)

		colour := colDim
		if ebiten.IsStandardGamepadButtonPressed(id, b.button) {
			colour = colAccent
		}
		vector.DrawFilledRect(dst, x, y, 68, 22, colour, false)
		ebitenutil.DebugPrintAt(dst, b.name, int(x)+6, int(y)+7)
	}

	// Sticks, drawn where they are pushed, so a screenshot shows the direction.
	for i, stick := range []struct {
		label  string
		x, y   ebiten.StandardGamepadAxis
		centre [2]float32
	}{
		{"left", ebiten.StandardGamepadAxisLeftStickHorizontal, ebiten.StandardGamepadAxisLeftStickVertical, [2]float32{130, 250}},
		{"right", ebiten.StandardGamepadAxisRightStickHorizontal, ebiten.StandardGamepadAxisRightStickVertical, [2]float32{330, 250}},
	} {
		dx := float32(ebiten.StandardGamepadAxisValue(id, stick.x)) * 34
		dy := float32(ebiten.StandardGamepadAxisValue(id, stick.y)) * 34

		vector.StrokeCircle(dst, stick.centre[0], stick.centre[1], 38, 1, colDim, true)
		vector.DrawFilledCircle(dst, stick.centre[0]+dx, stick.centre[1]+dy, 12, colAccent, true)
		ebitenutil.DebugPrintAt(dst, stick.label, int(stick.centre[0])-14, int(stick.centre[1])+44)
		_ = i
	}
}
