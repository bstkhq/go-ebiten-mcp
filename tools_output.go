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
