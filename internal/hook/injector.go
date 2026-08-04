//go:build !ebitenmcp_nohook

package hook

import (
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
)

// Touch is one synthetic finger on the screen, in the game's logical pixels.
type Touch struct {
	ID int
	X  int
	Y  int
}

// press tracks one held key or mouse button across ticks.
//
// The stamp assigned when the press starts is reused on every later tick rather
// than refreshed. Ebitengine derives "just pressed" from the tick encoded in
// that stamp, so re-stamping a held key would make IsKeyJustPressed fire on
// every single tick of the press.
type press struct {
	pressedAt  inputTime
	releasedAt inputTime
	wantUp     bool
	doneTick   int64
}

// Injector accumulates the input a caller wants the game to observe and writes
// it into Ebitengine's state once per tick.
//
// Callers may touch it from any goroutine; Apply must run inside the game loop,
// before the game's own Update. Everything is stamped there rather than at call
// time, so a request that arrives mid-frame lands in the tick the game is about
// to read instead of a tick that has already gone by.
type Injector struct {
	mu      sync.Mutex
	keys    map[ebiten.Key]*press
	buttons map[ebiten.MouseButton]*press
	cursor  *[2]float64
	wheel   [2]float64
	touches []Touch
	runes   []rune
}

// NewInjector returns an idle Injector. Until something is pressed it never
// writes to Ebitengine's input state at all, so a game nobody is driving
// behaves exactly as if this package were not linked in.
func NewInjector() *Injector {
	return &Injector{
		keys:    map[ebiten.Key]*press{},
		buttons: map[ebiten.MouseButton]*press{},
	}
}

// resolveKey maps Ebitengine's virtual modifiers onto a physical key.
//
// KeyShift, KeyControl, KeyAlt and KeyMeta have slots in the array but are
// never read from them: IsKeyPressed answers them by OR-ing the left and right
// variants. Writing the virtual slot would look like it worked and do nothing.
func resolveKey(k ebiten.Key) ebiten.Key {
	switch k {
	case ebiten.KeyAlt:
		return ebiten.KeyAltLeft
	case ebiten.KeyControl:
		return ebiten.KeyControlLeft
	case ebiten.KeyShift:
		return ebiten.KeyShiftLeft
	case ebiten.KeyMeta:
		return ebiten.KeyMetaLeft
	}
	return k
}

// KeyDown starts holding a key. Holding an already-held key is a no-op, so the
// press keeps its original stamp and its duration keeps growing.
func (i *Injector) KeyDown(k ebiten.Key) {
	k = resolveKey(k)
	if k < 0 || k > ebiten.KeyMax {
		return
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	if p, ok := i.keys[k]; ok && !p.wantUp {
		return
	}
	i.keys[k] = &press{}
}

// KeyUp releases a held key. The release is stamped by the next Apply, so a key
// pressed and released between two ticks still produces one visible press.
func (i *Injector) KeyUp(k ebiten.Key) {
	k = resolveKey(k)

	i.mu.Lock()
	defer i.mu.Unlock()

	if p, ok := i.keys[k]; ok {
		p.wantUp = true
	}
}

// MouseDown starts holding a mouse button.
func (i *Injector) MouseDown(b ebiten.MouseButton) {
	if b < 0 || b > ebiten.MouseButtonMax {
		return
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	if p, ok := i.buttons[b]; ok && !p.wantUp {
		return
	}
	i.buttons[b] = &press{}
}

// MouseUp releases a held mouse button.
func (i *Injector) MouseUp(b ebiten.MouseButton) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if p, ok := i.buttons[b]; ok {
		p.wantUp = true
	}
}

// MoveCursor pins the cursor to a position in the game's logical pixels.
//
// It is re-applied every tick because the window backend overwrites the cursor
// from the real pointer on each frame; a single write would be erased before
// the game ever saw it.
func (i *Injector) MoveCursor(x, y float64) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.cursor = &[2]float64{x, y}
}

// ReleaseCursor hands the cursor back to the real pointer.
func (i *Injector) ReleaseCursor() {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.cursor = nil
}

// Scroll queues a wheel movement. Like a real wheel event it is visible for one
// tick only.
func (i *Injector) Scroll(x, y float64) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.wheel[0] += x
	i.wheel[1] += y
}

// SetTouches replaces the set of active touches. Passing none ends them all.
func (i *Injector) SetTouches(ts []Touch) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.touches = append(i.touches[:0], ts...)
}

// Type queues runes for the game's text input. Like real typing they are
// visible for one tick only.
func (i *Injector) Type(rs []rune) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.runes = append(i.runes, rs...)
}

// ReleaseAll drops every held key, button, touch and the pinned cursor. The
// releases are still stamped properly, so the game sees a real release rather
// than input that vanishes.
func (i *Injector) ReleaseAll() {
	i.mu.Lock()
	defer i.mu.Unlock()

	for _, p := range i.keys {
		p.wantUp = true
	}
	for _, p := range i.buttons {
		p.wantUp = true
	}
	i.touches = i.touches[:0]
	i.cursor = nil
}

// Idle reports whether the injector currently wants nothing, which lets the
// caller skip Apply entirely.
func (i *Injector) Idle() bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	return len(i.keys) == 0 && len(i.buttons) == 0 && i.cursor == nil &&
		i.wheel == [2]float64{} && len(i.touches) == 0 && len(i.runes) == 0
}

// Apply writes the accumulated input into Ebitengine's state for the current
// tick. It must be called from the game loop, before the game's Update.
//
// Everything is rewritten on every tick because the backend resets the state at
// the top of each one (internal/ui/context.go calls readInputState right before
// Update). Nothing survives on its own.
func (i *Injector) Apply() {
	i.mu.Lock()
	defer i.mu.Unlock()

	tick := Tick()

	// Retire presses whose release was already visible for its tick, before
	// writing anything, so a finished press leaves no trace behind.
	for k, p := range i.keys {
		if p.releasedAt != 0 && p.doneTick < tick {
			delete(i.keys, k)
		}
	}
	for b, p := range i.buttons {
		if p.releasedAt != 0 && p.doneTick < tick {
			delete(i.buttons, b)
		}
	}

	update(func(s *inputState) {
		for k, p := range i.keys {
			p.apply(tick, &s.KeyPressedTimes[k], &s.KeyReleasedTimes[k])
		}
		for b, p := range i.buttons {
			p.apply(tick, &s.MouseButtonPressedTimes[b], &s.MouseButtonReleasedTimes[b])
		}

		if i.cursor != nil {
			s.CursorX, s.CursorY = i.cursor[0], i.cursor[1]
		}
		if i.wheel != [2]float64{} {
			s.WheelX, s.WheelY = i.wheel[0], i.wheel[1]
		}
		if len(i.touches) > 0 {
			s.Touches = s.Touches[:0]
			for _, t := range i.touches {
				s.Touches = append(s.Touches, touch(t))
			}
		}
		if len(i.runes) > 0 {
			s.Runes = append(s.Runes, i.runes...)
		}
	})

	// Wheel and runes are events, not states: one tick and gone.
	i.wheel = [2]float64{}
	i.runes = i.runes[:0]
}

// apply writes one press into its pair of slots, stamping the start and the
// release the first time each is needed.
func (p *press) apply(tick int64, pressed, released *inputTime) {
	if p.pressedAt == 0 {
		p.pressedAt = stamp()
	}
	*pressed = p.pressedAt

	if p.wantUp && p.releasedAt == 0 {
		p.releasedAt = stamp()
		p.doneTick = tick
	}
	if p.releasedAt != 0 {
		*released = p.releasedAt
	}
}
