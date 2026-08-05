package ebitenmcp

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/hajimehoshi/ebiten/v2"
)

// Inspect turns a live value into plain JSON-able data.
//
// It reads unexported fields. That is the whole point: a Go game keeps
// virtually all of its state unexported, and an inspector that respected
// visibility would show an empty struct and call it a state dump. Reading is
// done through reflect.NewAt on an addressable value, which is why the root has
// to be a pointer to be useful.
//
// The limits are not decoration. A game object reaches half the heap through
// pointers, so without a depth bound, an item cap and cycle detection the first
// call would try to serialise the world.
type Inspect struct {
	// MaxDepth is how far down the tree to walk. Zero means the default.
	MaxDepth int

	// MaxItems caps the elements taken from any one slice, array or map.
	MaxItems int

	// MaxNodes is the total budget, which stops a wide-but-shallow structure
	// from being just as expensive as a deep one.
	MaxNodes int
}

const (
	defaultMaxDepth = 6
	defaultMaxItems = 50
	defaultMaxNodes = 5000
)

// Truncated marks where the walk stopped, so a reader can tell a value that is
// genuinely absent from one that was too expensive to include.
type Truncated struct {
	Truncated string `json:"__truncated"`
}

type walker struct {
	limits Inspect
	nodes  int
	seen   map[uintptr]bool
}

// Value walks v and returns something that marshals to JSON.
func (in Inspect) Value(v any) any {
	return in.value(reflect.ValueOf(v))
}

// At walks the value found at a dotted path: "screens.player.pos.x", with
// "[2]" for slice and array elements.
func (in Inspect) At(v any, path string) (any, error) {
	rv, err := resolvePath(reflect.ValueOf(v), path)
	if err != nil {
		return nil, err
	}
	return in.value(rv), nil
}

// value is the entry point that keeps a reflect.Value rather than round-
// tripping through an interface, which an unexported field cannot survive.
func (in Inspect) value(rv reflect.Value) any {
	if in.MaxDepth == 0 {
		in.MaxDepth = defaultMaxDepth
	}
	if in.MaxItems == 0 {
		in.MaxItems = defaultMaxItems
	}
	if in.MaxNodes == 0 {
		in.MaxNodes = defaultMaxNodes
	}

	w := &walker{limits: in, seen: map[uintptr]bool{}}
	return w.walk(rv, 0)
}

func (w *walker) walk(v reflect.Value, depth int) any {
	if !v.IsValid() {
		return nil
	}

	w.nodes++
	if w.nodes > w.limits.MaxNodes {
		return Truncated{"node budget exhausted"}
	}
	if depth > w.limits.MaxDepth {
		return Truncated{"max depth " + strconv.Itoa(w.limits.MaxDepth)}
	}

	if short, ok := shortcut(v); ok {
		return short
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		if v.Kind() == reflect.Pointer {
			addr := v.Pointer()
			if w.seen[addr] {
				return Truncated{"cycle"}
			}
			w.seen[addr] = true
			defer delete(w.seen, addr)
		}
		return w.walk(v.Elem(), depth)

	case reflect.Struct:
		return w.walkStruct(v, depth)

	case reflect.Slice, reflect.Array:
		return w.walkList(v, depth)

	case reflect.Map:
		return w.walkMap(v, depth)

	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return fmt.Sprintf("<%s>", v.Kind())

	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.String:
		return v.String()

	default:
		return fmt.Sprintf("<%s>", v.Kind())
	}
}

func (w *walker) walkStruct(v reflect.Value, depth int) any {
	out := map[string]any{}
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fv := exported(v.Field(i))

		if !fv.IsValid() {
			out[field.Name] = "<unreadable>"
			continue
		}
		out[field.Name] = w.walk(fv, depth+1)
	}
	return out
}

func (w *walker) walkList(v reflect.Value, depth int) any {
	n := v.Len()
	shown := min(n, w.limits.MaxItems)

	out := make([]any, 0, shown+1)
	for i := 0; i < shown; i++ {
		out = append(out, w.walk(v.Index(i), depth+1))
	}
	if shown < n {
		out = append(out, Truncated{fmt.Sprintf("%d more of %d", n-shown, n)})
	}
	return out
}

func (w *walker) walkMap(v reflect.Value, depth int) any {
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface())
	})

	out := map[string]any{}
	for i, k := range keys {
		if i >= w.limits.MaxItems {
			out["__truncated"] = fmt.Sprintf("%d more of %d", len(keys)-i, len(keys))
			break
		}
		out[fmt.Sprint(k.Interface())] = w.walk(v.MapIndex(k), depth+1)
	}
	return out
}

// shortcut renders the types where walking the fields would be worse than
// useless: an ebiten.Image would drag in the whole graphics stack, and a
// time.Time as a struct of wall clock bits helps nobody.
func shortcut(v reflect.Value) (any, bool) {
	if !v.IsValid() {
		return nil, false
	}

	switch v.Type().String() {
	case "time.Time":
		if !v.CanInterface() {
			return "<time.Time>", true
		}
		if t, ok := v.Interface().(time.Time); ok {
			return t.Format(time.RFC3339Nano), true
		}
	case "time.Duration":
		return time.Duration(v.Int()).String(), true
	case "*ebiten.Image", "ebiten.Image":
		return "<ebiten.Image>", true
	case "sync.Mutex", "sync.RWMutex", "sync.Once", "sync.WaitGroup":
		return "<" + v.Type().String() + ">", true
	}
	return nil, false
}

// exported makes an unexported field readable.
//
// reflect refuses to hand over the value of an unexported field, but the memory
// is right there and addressable whenever the walk started from a pointer.
// Re-deriving the value at the same address through an exported path is the
// standard way round it, and a debugger has no business pretending it cannot
// see private state.
func exported(v reflect.Value) reflect.Value {
	if v.IsValid() && !v.CanInterface() && v.CanAddr() {
		return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	}
	return v
}

// resolvePath walks down a dotted path, following pointers and interfaces along
// the way so callers do not have to spell out indirections.
func resolvePath(v reflect.Value, path string) (reflect.Value, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return v, nil
	}

	for _, step := range splitPath(path) {
		var err error
		if v, err = resolveStep(v, step); err != nil {
			return reflect.Value{}, err
		}
	}
	return v, nil
}

func resolveStep(v reflect.Value, step string) (reflect.Value, error) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return reflect.Value{}, fmt.Errorf("path stops at a nil value before %q", step)
		}
		v = v.Elem()
	}

	if i, ok := indexStep(step); ok {
		switch v.Kind() {
		case reflect.Slice, reflect.Array:
			if i < 0 || i >= v.Len() {
				return reflect.Value{}, fmt.Errorf("index %d is out of range, length is %d", i, v.Len())
			}
			return exported(v.Index(i)), nil
		default:
			return reflect.Value{}, fmt.Errorf("cannot index a %s with [%d]", v.Kind(), i)
		}
	}

	switch v.Kind() {
	case reflect.Struct:
		f := v.FieldByName(step)
		if !f.IsValid() {
			return reflect.Value{}, fmt.Errorf("%s has no field %q; it has %s",
				v.Type(), step, strings.Join(fieldNames(v.Type()), ", "))
		}
		return exported(f), nil

	case reflect.Map:
		key, err := mapKey(v.Type().Key(), step)
		if err != nil {
			return reflect.Value{}, err
		}
		entry := v.MapIndex(key)
		if !entry.IsValid() {
			return reflect.Value{}, fmt.Errorf("no map entry %q", step)
		}
		return entry, nil

	default:
		return reflect.Value{}, fmt.Errorf("cannot look up %q inside a %s", step, v.Kind())
	}
}

// splitPath turns "a.b[2].c" into a, b, [2], c.
func splitPath(path string) []string {
	var steps []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			steps = append(steps, current.String())
			current.Reset()
		}
	}

	for _, r := range path {
		switch r {
		case '.':
			flush()
		case '[':
			flush()
			current.WriteRune(r)
		case ']':
			current.WriteRune(r)
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()

	return steps
}

func indexStep(step string) (int, bool) {
	if !strings.HasPrefix(step, "[") || !strings.HasSuffix(step, "]") {
		return 0, false
	}
	i, err := strconv.Atoi(step[1 : len(step)-1])
	return i, err == nil
}

func mapKey(t reflect.Type, s string) (reflect.Value, error) {
	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf(s).Convert(t), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("map key %q is not a number", s)
		}
		return reflect.ValueOf(n).Convert(t), nil
	default:
		return reflect.Value{}, fmt.Errorf("cannot address a map keyed by %s with %q", t, s)
	}
}

func fieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names = append(names, t.Field(i).Name)
	}
	return names
}

// StateProvider is a named view a game publishes of itself, so an inspection can
// return "the podium, as the game understands it" rather than a struct dump the
// caller has to interpret.
//
// It is handed the game that is running now. That is the whole reason this is
// not a plain func() any: the registry used to be global and the function
// captured whatever game existed when it was written, so after a game_reset
// built a fresh one, @summary went on describing the object nobody was playing
// any more — and looked entirely plausible doing it.
//
// It runs inside the game loop, so it can read state without locking, and it
// must not block.
type StateProvider func(game ebiten.Game) any

// stateProviders is what a game has published about itself.
//
// Its own type with its own lock: it is a map somebody writes once at startup
// and the tools read, with no bearing on whether the game is running, paused or
// crashed. The game it hands the provider comes from the runtime at call time,
// which is the one thing it does need and the reason that is a parameter.
type stateProviders struct {
	mu sync.Mutex
	by map[string]StateProvider
}

func (s *stateProviders) register(name string, fn StateProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.by == nil {
		s.by = map[string]StateProvider{}
	}
	s.by[name] = fn
}

func (s *stateProviders) unregister(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.by, name)
}

func (s *stateProviders) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	names := make([]string, 0, len(s.by))
	for name := range s.by {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

func (s *stateProviders) get(name string) (StateProvider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, ok := s.by[name]
	return fn, ok
}

// RegisterState publishes a named snapshot. Registering the same name twice
// replaces it.
func (r *Runtime) RegisterState(name string, fn StateProvider) {
	r.states.register(name, fn)
}

// UnregisterState removes one.
func (r *Runtime) UnregisterState(name string) { r.states.unregister(name) }

func (r *Runtime) stateProviderNames() []string { return r.states.names() }

// callStateProvider runs one against the game that is running now. Must be
// called from inside the loop.
func (r *Runtime) callStateProvider(name string) (any, bool) {
	fn, ok := r.states.get(name)
	if !ok {
		return nil, false
	}
	return fn(r.currentGame()), true
}
