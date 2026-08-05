package ebitenmcp

import (
	"context"
	"fmt"
	"image"
	"os"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultToolTimeout bounds every tool that has to go through the game loop.
//
// There is no such thing as an unbounded wait here. The loop being stuck is one
// of the things this server exists to diagnose, and a tool that hung along with
// it would take the diagnosis down too.
const defaultToolTimeout = 5 * time.Second

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultToolTimeout)
}

// slowTick is what one tick is assumed to cost at worst.
//
// Far more than the 16ms of sixty a second, because the machines this runs on
// are exactly the ones where that is not true: a software rasteriser, a shared
// CI box, a game being stepped through. Guessing low turns a slow frame into a
// reported failure.
const slowTick = 100 * time.Millisecond

// tickBudget bounds a tool by the work it was asked to do rather than by a flat
// number.
//
// game_step used a flat five seconds while every sibling scaled, so a step of
// more than about three hundred ticks always timed out — and then reported that
// the game loop was not running, when it had been stepping exactly as told. One
// formula in one place is how that stops being possible to get wrong again.
func tickBudget(ctx context.Context, ticks int) (context.Context, context.CancelFunc) {
	if ticks < 0 {
		ticks = 0
	}
	return context.WithTimeout(ctx, time.Duration(ticks)*slowTick+defaultToolTimeout)
}

func (s *Server) addViewTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_screenshot",
		Description: "Capture the next frame the game draws, exactly as the player sees it. " +
			"Returns the image inline, plus the path and URL of a lossless PNG on disk.",
		Annotations: readOnly("Screenshot"),
	}, s.screenshot)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_record",
		Description: "Record the next N ticks. Returns a contact sheet inline — a grid of " +
			"sampled frames labelled with their tick, which is how motion can actually be " +
			"read — and writes a video file alongside it for a person to watch.",
		Annotations: readOnly("Record"),
	}, s.record)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_compare",
		Description: "Put two captures side by side with their difference highlighted. " +
			"Capture a 'before', change something, then call this to show what changed.",
		Annotations: readOnly("Compare frames"),
	}, s.compare)
}

// ---------------------------------------------------------------------------
// game_screenshot
// ---------------------------------------------------------------------------

// stageDoc describes the stage argument, once.
//
// It is a Go constant and not a jsonschema tag because a tag has to be a
// literal, so four tools had this paragraph copied into them — with one of the
// copies stating the opposite default, and a comment above them claiming it was
// written once. The tags now say `jsonschema:"which drawing step to read: final for what the player sees, offscreen for what the game's own Draw produced before its DrawFinalScreen ran. Only differs for a game that draws its own final screen. Defaults to final"` and the text is attached
// to the schema after the fact, in stageSchema, which is the only way to have
// one copy of it.
const stageDoc = "which drawing step to read: 'final' for what the player sees, " +
	"'offscreen' for what the game's own Draw produced before its DrawFinalScreen " +
	"ran. Only differs for a game that draws its own final screen — where asking " +
	"for both is how you tell whether a visual bug is in the game or in that pass. " +
	"Defaults to final"

type screenshotInput struct {
	WaitTicks   int    `json:"wait_ticks,omitempty" jsonschema:"let the game run this many ticks first"`
	MaxSize     int    `json:"max_size,omitempty" jsonschema:"longest side of the inline image in pixels; defaults to 1024. The file on disk is always full resolution"`
	FullQuality bool   `json:"full_quality,omitempty" jsonschema:"return the inline image at native resolution, however large that is"`
	Stage       string `json:"stage,omitempty" jsonschema:"which drawing step to read: final for what the player sees, offscreen for what the game's own Draw produced before its DrawFinalScreen ran. Only differs for a game that draws its own final screen. Defaults to final"`
}

func (s *Server) screenshot(ctx context.Context, _ *mcpsdk.CallToolRequest, in screenshotInput) (*mcpsdk.CallToolResult, any, error) {
	stage, err := s.stage(in.Stage)
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := withTimeout(ctx)
	defer cancel()

	if in.WaitTicks > 0 {
		if err := s.rt.WaitTicks(ctx, in.WaitTicks); err != nil {
			return nil, nil, s.stalled(err)
		}
	}

	frame, err := s.rt.CaptureStage(ctx, stage)
	if err != nil {
		return nil, nil, s.stalled(err)
	}

	return s.frameResult("shot", frame, in.MaxSize, in.FullQuality, "")
}

// stage turns the argument into a Stage, defaulting to whatever the game was
// configured with.
func (s *Server) stage(name string) (Stage, error) {
	switch Stage(name) {
	case "":
		return s.rt.DefaultStage(), nil
	case StageFinal:
		return StageFinal, nil
	case StageOffscreen:
		return StageOffscreen, nil
	default:
		return "", fmt.Errorf("unknown stage %q: %s", name, stageDoc)
	}
}

// frameResult produces the three forms every capture comes in: inline for the
// conversation, a file for a person, and a URL for a client that renders one.
func (s *Server) frameResult(prefix string, frame *Frame, maxSize int, full bool, note string) (*mcpsdk.CallToolResult, any, error) {
	art, err := s.media.savePNG(prefix, frame.Image)
	if err != nil {
		return nil, nil, err
	}

	text := fmt.Sprintf("tick %d, %dx%d native", frame.Tick, art.Width, art.Height)
	// Only worth saying when the two stages are actually different images.
	// Naming a distinction that does not exist for this game would invite
	// somebody to go looking for it.
	if s.rt.HasFinalPass() {
		text += ", " + string(frame.Stage)
	}
	if note != "" {
		text += "\n" + note
	}

	result, shown, err := imageResult(frame.Image, art, text, maxSize, full)
	if err != nil {
		return nil, nil, err
	}

	return result, map[string]any{
		"tick":     frame.Tick,
		"stage":    string(frame.Stage),
		"artifact": art,
		"inline":   map[string]int{"width": shown.Dx(), "height": shown.Dy()},
	}, nil
}

// imageResult is the shape every tool that returns a picture returns: the image
// inline for the conversation, and the path of the full-resolution file for a
// person.
//
// It was written out four times over, and the cost of that was not tidiness: the
// only copy that honoured max_size and full_quality was the one in
// game_screenshot, so those two arguments quietly did nothing on the four tools
// that return the *bigger* images. Now they work everywhere, because there is
// one place for them to work.
func imageResult(img image.Image, art *Artifact, note string, maxSize int, full bool) (*mcpsdk.CallToolResult, image.Rectangle, error) {
	var inline image.Image = img
	if !full {
		if maxSize <= 0 {
			maxSize = inlineMaxSize
		}
		inline = fitInline(img, maxSize)
	}

	data, err := encodePNG(inline)
	if err != nil {
		return nil, image.Rectangle{}, err
	}

	shown := inline.Bounds()
	if shown.Dx() != art.Width {
		note += fmt.Sprintf("\nshown at %dx%d of %dx%d", shown.Dx(), shown.Dy(), art.Width, art.Height)
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ImageContent{Data: data, MIMEType: "image/png"},
			&mcpsdk.TextContent{Text: note + "\n" + art.Path},
		},
	}, shown, nil
}

// ---------------------------------------------------------------------------
// game_record
// ---------------------------------------------------------------------------

// maxRecordFrames is a minute at sixty a second. It is a sanity bound on the
// argument, not the thing that stops a recording running the machine out of
// memory: 3600 frames at 720p is 12 GiB and at 4K is 111 GiB, so the real limit
// is recordBudget, counted in bytes once the first frame has said how big a
// frame is. (An earlier comment here said 2 GB at 720p. It was out by six.)
const maxRecordFrames = 3600

// recordBudget is what a recording may hold in memory at once.
//
// Every frame is kept until the video is written, so this is the number that
// matters. Generous enough for several seconds of 1080p, small enough that
// getting the arguments wrong is an answer rather than an OOM kill.
const recordBudget = 512 << 20

type recordInput struct {
	Frames  int    `json:"frames,omitempty" jsonschema:"how many drawn frames to record; defaults to 60. Note that a frame is one Draw, which without vsync can happen several times per tick"`
	Every   int    `json:"every,omitempty" jsonschema:"keep one frame out of every N captured; defaults to 1"`
	Columns int    `json:"columns,omitempty" jsonschema:"columns in the contact sheet; defaults to 4"`
	Cells   int    `json:"cells,omitempty" jsonschema:"how many frames to put in the contact sheet; defaults to 12"`
	Format  string `json:"format,omitempty" jsonschema:"mp4 for something to watch, gif for something that plays inline in a README or an issue; defaults to mp4"`
	Inline  string `json:"inline,omitempty" jsonschema:"what to return in the reply: 'sheet' for a grid of frames with their ticks, or 'gif' for the animation itself, which some clients play and some show as a single frame; defaults to sheet"`
	NoVideo bool   `json:"no_video,omitempty" jsonschema:"skip writing the video file and only produce the contact sheet"`
	MaxSize int    `json:"max_size,omitempty" jsonschema:"longest side of the inline contact sheet in pixels; defaults to 1024. The file on disk is always full resolution"`

	FullQuality bool   `json:"full_quality,omitempty" jsonschema:"return the inline contact sheet at native resolution, however large that is"`
	Stage       string `json:"stage,omitempty" jsonschema:"which drawing step to read: final for what the player sees, offscreen for what the game's own Draw produced before its DrawFinalScreen ran. Only differs for a game that draws its own final screen. Defaults to final"`
}

func (s *Server) record(ctx context.Context, _ *mcpsdk.CallToolRequest, in recordInput) (*mcpsdk.CallToolResult, any, error) {
	stage, err := s.stage(in.Stage)
	if err != nil {
		return nil, nil, err
	}
	if err := in.fill(); err != nil {
		return nil, nil, err
	}

	// Recording is the one thing that legitimately takes a while, so the budget
	// follows the request instead of the default.
	ctx, cancel := tickBudget(ctx, in.Frames)
	defer cancel()

	frames, capped, err := s.recordFrames(ctx, stage, in.Frames, in.Every)
	if err != nil {
		return nil, nil, err
	}

	sheet := contactSheet(pick(frames, in.Cells), in.Columns)

	sheetArt, err := s.media.savePNG("sheet", sheet)
	if err != nil {
		return nil, nil, err
	}

	out := map[string]any{
		"frames":        len(frames),
		"stage":         string(stage),
		"first_tick":    frames[0].Tick,
		"last_tick":     frames[len(frames)-1].Tick,
		"ticks_covered": frames[len(frames)-1].Tick - frames[0].Tick,
		"contact_sheet": sheetArt,
	}

	note := fmt.Sprintf("%d frames covering ticks %d to %d",
		len(frames), frames[0].Tick, frames[len(frames)-1].Tick)
	if capped != "" {
		note += "\n" + capped
		out["capped"] = capped
	}

	if !in.NoVideo {
		note += s.attachVideo(ctx, frames, in.Format, out)
	}

	if in.Inline == "gif" {
		result, why := inlineGif(out, note, sheetArt)
		if result != nil {
			return result, out, nil
		}
		note += why
	}

	result, _, err := imageResult(sheet, sheetArt, note, in.MaxSize, in.FullQuality)
	if err != nil {
		return nil, nil, err
	}
	return result, out, nil
}

// attachVideo writes the video and returns what to add to the note. A failure
// to encode is a line in the answer rather than a failed call: the frames are
// the point, and the contact sheet already has them.
func (s *Server) attachVideo(ctx context.Context, frames []*Frame, format string, out map[string]any) string {
	video, err := s.saveVideo(ctx, frames, format)
	if err != nil {
		return "\nno video: " + err.Error()
	}

	out["video"] = video

	note := "\nvideo: " + video.Path
	if video.URL != "" {
		note += "\n" + video.URL
	}
	return note
}

// inlineGif returns the animation itself for a client that plays it, or nothing
// and a line saying why not.
//
// Where a client does animate one it beats a grid of stills at showing motion;
// where one does not it shows a single frame, and then the contact sheet is the
// better answer. Which is why this is asked for rather than assumed.
func inlineGif(out map[string]any, note string, sheet *Artifact) (*mcpsdk.CallToolResult, string) {
	video, ok := out["video"].(*Artifact)
	if !ok || video.Kind != "gif" {
		return nil, "\nno gif was produced, so the contact sheet is what came back"
	}

	data, err := os.ReadFile(video.Path)
	if err != nil {
		return nil, "\ncould not read the gif back: " + err.Error()
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ImageContent{Data: data, MIMEType: "image/gif"},
			&mcpsdk.TextContent{Text: note + "\n" + sheet.Path},
		},
	}, ""
}

// fill applies the defaults and refuses the requests that would end the game
// rather than answer it.
func (in *recordInput) fill() error {
	if in.Frames <= 0 {
		in.Frames = 60
	}
	// Capped, because every frame recorded is held in memory at full resolution
	// until the video is written — 3.5 MB each at 720p — and an agent that
	// mistook frames for ticks and asked for a hundred thousand would take the
	// game down with it rather than be told no.
	if in.Frames > maxRecordFrames {
		return fmt.Errorf("recording %d frames would hold them all in memory at once; "+
			"the limit is %d, and `every` covers a longer stretch for the same cost",
			in.Frames, maxRecordFrames)
	}
	if in.Every <= 0 {
		in.Every = 1
	}
	if in.Columns <= 0 {
		in.Columns = 4
	}
	if in.Cells <= 0 {
		in.Cells = 12
	}
	if in.Inline == "gif" && in.Format == "" {
		in.Format = "gif"
	}
	return nil
}

// recordFrames captures until it has what it was asked for or the loop stops
// answering.
//
// Stopping early with something is deliberate: a recording that ran into a crash
// halfway is exactly the recording somebody wants, and refusing to return it
// because it is short would throw away the evidence.
func (s *Server) recordFrames(ctx context.Context, stage Stage, count, every int) ([]*Frame, string, error) {
	frames := make([]*Frame, 0, count/every+1)

	var (
		held  int
		note  string
		bytes int
	)

	for i := 0; i < count; i++ {
		// Skipped frames are skipped before the capture, not after it. Asking
		// for one and throwing it away still costs a synchronisation with the
		// GPU and a full-resolution allocation, so `every: 60` used to pay for
		// sixty frames to keep one.
		if i%every != 0 {
			if err := s.rt.WaitTicks(ctx, 1); err != nil {
				break
			}
			continue
		}

		frame, err := s.rt.CaptureStage(ctx, stage)
		if err != nil {
			if len(frames) == 0 {
				return nil, "", s.stalled(err)
			}
			break
		}

		if bytes == 0 {
			b := frame.Image.Bounds()
			bytes = b.Dx() * b.Dy() * 4
		}
		if held+bytes > recordBudget {
			note = fmt.Sprintf("stopped at %d frames: %d more would pass the %d MB a recording "+
				"may hold at %dx%d. Raise `every` to cover the same stretch for less",
				len(frames), count-i, recordBudget>>20,
				frame.Image.Bounds().Dx(), frame.Image.Bounds().Dy())
			break
		}
		held += bytes

		frames = append(frames, frame)
	}

	if len(frames) == 0 {
		return nil, "", fmt.Errorf("recorded nothing: the game drew no frames")
	}
	return frames, note, nil
}

// pick spreads n choices evenly across the recording, so the sheet covers the
// whole span rather than the first n ticks of it.
func pick(frames []*Frame, n int) []*Frame {
	if n >= len(frames) {
		return frames
	}

	out := make([]*Frame, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, frames[i*len(frames)/n])
	}
	return out
}

// ---------------------------------------------------------------------------
// game_compare
// ---------------------------------------------------------------------------

type compareInput struct {
	WaitTicks   int  `json:"wait_ticks,omitempty" jsonschema:"ticks to let pass between the two captures; defaults to 30"`
	MaxSize     int  `json:"max_size,omitempty" jsonschema:"longest side of the inline image in pixels; defaults to 1024"`
	FullQuality bool `json:"full_quality,omitempty" jsonschema:"return the inline image at native resolution"`

	Stage string `json:"stage,omitempty" jsonschema:"which drawing step to read: final for what the player sees, offscreen for what the game's own Draw produced before its DrawFinalScreen ran. Only differs for a game that draws its own final screen. Defaults to final"`
}

func (s *Server) compare(ctx context.Context, _ *mcpsdk.CallToolRequest, in compareInput) (*mcpsdk.CallToolResult, any, error) {
	stage, err := s.stage(in.Stage)
	if err != nil {
		return nil, nil, err
	}

	if in.WaitTicks <= 0 {
		in.WaitTicks = 30
	}

	ctx, cancel := tickBudget(ctx, in.WaitTicks)
	defer cancel()

	before, err := s.rt.CaptureStage(ctx, stage)
	if err != nil {
		return nil, nil, s.stalled(err)
	}

	if err := s.rt.WaitTicks(ctx, in.WaitTicks); err != nil {
		return nil, nil, s.stalled(err)
	}

	after, err := s.rt.CaptureStage(ctx, stage)
	if err != nil {
		return nil, nil, s.stalled(err)
	}

	comparison, changed := compareImages(before.Image, after.Image)

	art, err := s.media.savePNG("compare", comparison)
	if err != nil {
		return nil, nil, err
	}

	total := before.Image.Bounds().Dx() * before.Image.Bounds().Dy()
	note := fmt.Sprintf("tick %d vs %d: %d of %d pixels differ (%.2f%%)",
		before.Tick, after.Tick, changed, total, 100*float64(changed)/float64(total))

	result, _, err := imageResult(comparison, art, note, in.MaxSize, in.FullQuality)
	if err != nil {
		return nil, nil, err
	}

	return result, map[string]any{
		"before_tick":    before.Tick,
		"after_tick":     after.Tick,
		"changed_pixels": changed,
		"total_pixels":   total,
		"artifact":       art,
	}, nil
}

// stalled turns a loop timeout into an answer rather than a bare error: which
// tick it died on and how long ago is the actual diagnosis.
func (s *Server) stalled(err error) error {
	if err != ErrLoopStalled {
		return err
	}

	age := time.Since(s.rt.LastTick()).Round(time.Millisecond)
	paused, steps := s.rt.Paused()

	msg := fmt.Sprintf("the game loop is not running: last tick %d was %s ago", s.rt.Tick(), age)
	if paused {
		msg += fmt.Sprintf(" (paused, %d steps queued — call game_resume)", steps)
	}
	if crash := s.rt.Crash(); crash != nil {
		msg += fmt.Sprintf(" (the game panicked in %s: %s — see game_state)", crash.Phase, crash.Value)

		// The last few lines, here rather than only in game_state. An error that
		// sends you somewhere else to find out what happened costs a round trip
		// to say what it could have said itself.
		if lines := s.crashTraces(crash); len(lines) > 0 {
			if len(lines) > 3 {
				lines = lines[len(lines)-3:]
			}
			for _, l := range lines {
				msg += "\n  " + l.Text
			}
		}
	}
	return fmt.Errorf("%s", msg)
}
