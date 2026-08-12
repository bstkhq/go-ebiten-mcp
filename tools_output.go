package ebitenmcp

// What the tools return, as types.
//
// Every handler used to answer with a map[string]any, which meant the MCP tool
// had no OutputSchema: a client could not know what a result contains, could not
// validate one, and nothing anywhere would notice a key being renamed or
// quietly dropped. The SDK derives the schema from the handler's output type, so
// the fix is to have one.
//
// They live together rather than beside their handlers because that is how you
// see the shape of the whole surface at once, and how you notice that two tools
// call the same thing by different names.

// Artifacts is what a tool produced on disk.
type Artifacts struct {
	Artifact *Artifact `json:"artifact,omitempty"`
	Video    *Artifact `json:"video,omitempty"`
	Sheet    *Artifact `json:"contact_sheet,omitempty"`
}

// Size is a width and a height in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// FrameOutput is one captured frame: game_screenshot's answer, and the shape the
// others borrow.
type FrameOutput struct {
	Tick     int64     `json:"tick"`
	Stage    Stage     `json:"stage"`
	Artifact *Artifact `json:"artifact"`
	Inline   Size      `json:"inline"`
}

// RecordOutput is game_record's answer.
type RecordOutput struct {
	Frames       int       `json:"frames"`
	Stage        Stage     `json:"stage"`
	FirstTick    int64     `json:"first_tick"`
	LastTick     int64     `json:"last_tick"`
	TicksCovered int64     `json:"ticks_covered"`
	Sheet        *Artifact `json:"contact_sheet"`
	Video        *Artifact `json:"video,omitempty"`

	// Capped says the recording stopped short of what was asked for, and why.
	Capped string `json:"capped,omitempty"`
}

// CompareOutput is game_compare's answer.
type CompareOutput struct {
	BeforeTick    int64     `json:"before_tick"`
	AfterTick     int64     `json:"after_tick"`
	ChangedPixels int       `json:"changed_pixels"`
	TotalPixels   int       `json:"total_pixels"`
	Artifact      *Artifact `json:"artifact"`
}

// FramesOutput is game_frames' answer: either the buffer's status, or what it
// was holding.
type FramesOutput struct {
	Ring RingStatus `json:"ring"`
	Note string     `json:"note,omitempty"`

	Frames    int       `json:"frames,omitempty"`
	FirstTick int64     `json:"first_tick,omitempty"`
	LastTick  int64     `json:"last_tick,omitempty"`
	Sheet     *Artifact `json:"contact_sheet,omitempty"`
}

// RingStatus is what the retrospective buffer is doing. Reported by game_state
// as well, so that a buffer throwing away half of what it is given is visible
// rather than something to deduce from tick numbers.
type RingStatus struct {
	Enabled bool `json:"enabled"`

	Frames     int     `json:"frames,omitempty"`
	MemoryMB   float64 `json:"memory_mb,omitempty"`
	BudgetMB   float64 `json:"budget_mb,omitempty"`
	Every      int     `json:"every,omitempty"`
	Stage      Stage   `json:"stage,omitempty"`
	Encoded    int64   `json:"encoded,omitempty"`
	Dropped    int64   `json:"dropped,omitempty"`
	OldestTick int64   `json:"oldest_tick,omitempty"`
	NewestTick int64   `json:"newest_tick,omitempty"`
	Note       string  `json:"note,omitempty"`
}

// StateOutput is game_state's answer: how the game and the process are doing.
//
// A struct with three optional fields, not a map. It looked like a map's job
// because so much goes in it, but nothing about its shape depends on what the
// tool found — only its values do, and a field that is sometimes absent is what
// omitempty is for.
type StateOutput struct {
	Name          string `json:"name"`
	Loop          string `json:"loop"`
	Tick          int64  `json:"tick"`
	SinceLastTick string `json:"since_last_tick"`
	Uptime        string `json:"uptime"`
	Paused        bool   `json:"paused"`
	QueuedSteps   int    `json:"queued_steps"`

	TPS       int     `json:"tps"`
	ActualTPS float64 `json:"actual_tps"`
	ActualFPS float64 `json:"actual_fps"`
	VSync     bool    `json:"vsync"`

	Window            Size    `json:"window"`
	DeviceScaleFactor float64 `json:"device_scale_factor"`
	Screen            *Screen `json:"screen,omitempty"`

	Input          InputStatus `json:"input"`
	Go             GoStats     `json:"go"`
	StateProviders []string    `json:"state_providers"`
	MediaDir       string      `json:"media_dir"`
	FrameRing      RingStatus  `json:"frame_ring"`

	Crash       *Crash      `json:"crash,omitempty"`
	CrashTraces []TraceLine `json:"crash_traces,omitempty"`
}

// Screen is the last frame's size and when it was taken.
type Screen struct {
	Width          int   `json:"width"`
	Height         int   `json:"height"`
	CapturedAtTick int64 `json:"captured_at_tick"`
}

// InputStatus says whether synthetic input works, and why not when it does not.
type InputStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// GoStats is what the runtime says about itself.
type GoStats struct {
	Version    string  `json:"version"`
	Goroutines int     `json:"goroutines"`
	HeapMB     float64 `json:"heap_mb"`
	GCCycles   uint32  `json:"gc_cycles"`
}

// FrametimesOutput is game_frametimes' answer.
type FrametimesOutput struct {
	Ticks int `json:"ticks"`

	Update Percentiles `json:"update,omitempty"`
	Draw   Percentiles `json:"draw,omitempty"`
	Wall   Percentiles `json:"wall,omitempty"`

	ActualTPS float64 `json:"actual_tps,omitempty"`
	ActualFPS float64 `json:"actual_fps,omitempty"`

	// Reading is the sentence that says what the numbers mean, because the
	// interesting part of them is a comparison and not any one figure.
	Reading string `json:"reading,omitempty"`

	Timings []FrameTiming `json:"timings,omitempty"`
}

// Percentiles summarises one column of the frame timings. Durations as text,
// because "1.2ms" is what somebody reading this wants and a nanosecond count is
// not.
type Percentiles struct {
	Mean string `json:"mean"`
	P50  string `json:"p50"`
	P95  string `json:"p95"`
	P99  string `json:"p99"`
	Max  string `json:"max"`
}

// GoroutinesOutput is game_goroutines' answer. The dump itself comes back as
// text, since it is for reading rather than for parsing.
type GoroutinesOutput struct {
	Count int `json:"count"`
}

// InputStateOutput is game_input_state's answer: what the game currently
// believes about its input, read from inside the loop.
type InputStateOutput struct {
	Tick      int64       `json:"tick"`
	Injection InputStatus `json:"injection"`

	KeysPressed  []string       `json:"keys_pressed"`
	MouseButtons []string       `json:"mouse_buttons"`
	Touches      []touchPoint   `json:"touches"`
	Gamepads     []GamepadState `json:"gamepads"`

	Cursor Point  `json:"cursor"`
	Wheel  Offset `json:"wheel"`
}

// Point is a cursor position in the game's logical pixels.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Offset is a wheel movement.
type Offset struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// GamepadState is one controller as the game sees it.
//
// The SDL id is here because it is what a game switches on to decide what kind
// of controller this is, so seeing it is often the whole answer to "why is the
// game ignoring my gamepad".
type GamepadState struct {
	ID             int       `json:"id"`
	Name           string    `json:"name"`
	SDLID          string    `json:"sdl_id"`
	StandardLayout bool      `json:"standard_layout"`
	ButtonsPressed []int     `json:"buttons_pressed"`
	Axes           []float64 `json:"axes"`
}

// LoopOutput is what the tools that hold and release the game answer with.
type LoopOutput struct {
	Tick        int64  `json:"tick"`
	Paused      bool   `json:"paused"`
	QueuedSteps int    `json:"queued_steps,omitempty"`
	TPS         int    `json:"tps,omitempty"`
	Waited      string `json:"waited,omitempty"`
	Ran         int64  `json:"ran,omitempty"`
}

// WaitOutput is game_wait's answer when the condition was met.
type WaitOutput struct {
	Tick    int64  `json:"tick"`
	Value   any    `json:"value,omitempty"`
	Was     any    `json:"was,omitempty"`
	Matched string `json:"matched,omitempty"`
	Waited  string `json:"waited,omitempty"`
}

// InputOutput is what the tools that drive the game answer with: the tick the
// input landed in, and a description of what was done.
//
// One type for all of them rather than one each, because every field here is
// "what did this call do" and a client that wants to know reads the same place
// whichever tool it called. The empty ones are left out.
type InputOutput struct {
	Tick int64 `json:"tick"`

	Keys      []string  `json:"keys,omitempty"`
	Held      []string  `json:"held,omitempty"`
	Released  []string  `json:"released,omitempty"`
	Text      string    `json:"text,omitempty"`
	Touches   int       `json:"touches,omitempty"`
	MovedTo   []float64 `json:"moved_to,omitempty"`
	DraggedTo []float64 `json:"dragged_to,omitempty"`
	Scrolled  []float64 `json:"scrolled,omitempty"`
	Clicked   string    `json:"clicked,omitempty"`
	Holding   string    `json:"holding,omitempty"`
	Button    string    `json:"released_button,omitempty"`
	Cursor    string    `json:"cursor,omitempty"`

	// A gamepad call goes through the same finish, so its answers live here too
	// rather than in a type that would be this one with four fields added.
	Connected    *int   `json:"connected,omitempty"`
	SDLID        string `json:"sdl_id,omitempty"`
	GamepadID    *int   `json:"gamepad_id,omitempty"`
	Disconnected *int   `json:"disconnected,omitempty"`
}

// TPSOutput is game_set_tps' answer.
type TPSOutput struct {
	TPS int `json:"tps"`
}

// ResetOutput is game_reset's answer.
type ResetOutput struct {
	Tick  int64 `json:"tick"`
	Reset bool  `json:"reset"`
}

// InspectOutput is game_inspect's answer.
type InspectOutput struct {
	Tick  int64  `json:"tick"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// TracesOutput is game_traces' answer.
type TracesOutput struct {
	Lines []TraceLine `json:"lines"`
	Tick  int64       `json:"tick"`
	Total int         `json:"total"`

	// Unavailable says why these lines are not everything the process wrote:
	// no capture at all on a platform without descriptor duplication, or one
	// of the two streams missing. Absent when the capture is whole.
	Unavailable string `json:"unavailable,omitempty"`
}

// ProfileOutput is game_profile's answer.
type ProfileOutput struct {
	Kind     string    `json:"kind"`
	Tick     int64     `json:"tick"`
	Artifact *Artifact `json:"artifact"`
	Note     string    `json:"note,omitempty"`
}

// ScriptOutput is game_script's answer.
type ScriptOutput struct {
	Steps    int          `json:"steps"`
	Ticks    int64        `json:"ticks"`
	Tick     int64        `json:"tick"`
	Captured int          `json:"captured"`
	Log      []ScriptStep `json:"log"`
	Sheet    *Artifact    `json:"contact_sheet,omitempty"`
}

// ScriptStep is one line of a script's log: which step ran, when it was meant
// to, and when it did.
type ScriptStep struct {
	Step  int    `json:"step"`
	At    int    `json:"at"`
	Tick  int64  `json:"tick"`
	Label string `json:"label,omitempty"`
}
