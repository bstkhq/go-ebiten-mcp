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

// stageDoc is shared by every tool that captures, because the choice is the
// same one everywhere and describing it three different ways would suggest it
// was three different things.
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

	var inline image.Image = frame.Image
	if !full {
		if maxSize <= 0 {
			maxSize = inlineMaxSize
		}
		inline = fitInline(frame.Image, maxSize)
	}

	data, err := encodePNG(inline)
	if err != nil {
		return nil, nil, err
	}

	b := inline.Bounds()
	text := fmt.Sprintf("tick %d, %dx%d native", frame.Tick, art.Width, art.Height)
	if b.Dx() != art.Width {
		text += fmt.Sprintf(", shown at %dx%d", b.Dx(), b.Dy())
	}
	// Only worth saying when the two stages are actually different images.
	// Naming a distinction that does not exist for this game would invite
	// somebody to go looking for it.
	if s.rt.HasFinalPass() {
		text += ", " + string(frame.Stage)
	}
	if note != "" {
		text += "\n" + note
	}
	text += "\n" + art.Path

	out := map[string]any{
		"tick":     frame.Tick,
		"stage":    string(frame.Stage),
		"artifact": art,
		"inline":   map[string]int{"width": b.Dx(), "height": b.Dy()},
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ImageContent{Data: data, MIMEType: "image/png"},
			&mcpsdk.TextContent{Text: text},
		},
	}, out, nil
}

// ---------------------------------------------------------------------------
// game_record
// ---------------------------------------------------------------------------

type recordInput struct {
	Frames  int    `json:"frames,omitempty" jsonschema:"how many drawn frames to record; defaults to 60. Note that a frame is one Draw, which without vsync can happen several times per tick"`
	Every   int    `json:"every,omitempty" jsonschema:"keep one frame out of every N captured; defaults to 1"`
	Columns int    `json:"columns,omitempty" jsonschema:"columns in the contact sheet; defaults to 4"`
	Cells   int    `json:"cells,omitempty" jsonschema:"how many frames to put in the contact sheet; defaults to 12"`
	Format  string `json:"format,omitempty" jsonschema:"mp4 for something to watch, gif for something that plays inline in a README or an issue; defaults to mp4"`
	Inline  string `json:"inline,omitempty" jsonschema:"what to return in the reply: 'sheet' for a grid of frames with their ticks, or 'gif' for the animation itself, which some clients play and some show as a single frame; defaults to sheet"`
	NoVideo bool   `json:"no_video,omitempty" jsonschema:"skip writing the video file and only produce the contact sheet"`
	Stage   string `json:"stage,omitempty" jsonschema:"which drawing step to read: final for what the player sees, offscreen for what the game's own Draw produced before its DrawFinalScreen ran. Only differs for a game that draws its own final screen. Defaults to final"`
}

func (s *Server) record(ctx context.Context, _ *mcpsdk.CallToolRequest, in recordInput) (*mcpsdk.CallToolResult, any, error) {
	stage, err := s.stage(in.Stage)
	if err != nil {
		return nil, nil, err
	}

	if in.Frames <= 0 {
		in.Frames = 60
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

	// Recording is the one thing that legitimately takes a while, so the budget
	// follows the request instead of the default.
	budget := time.Duration(in.Frames)*100*time.Millisecond + defaultToolTimeout
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	frames := make([]*Frame, 0, in.Frames/in.Every+1)
	for i := 0; i < in.Frames; i++ {
		frame, err := s.rt.CaptureStage(ctx, stage)
		if err != nil {
			if len(frames) == 0 {
				return nil, nil, s.stalled(err)
			}
			break
		}
		if i%in.Every == 0 {
			frames = append(frames, frame)
		}
	}

	if len(frames) == 0 {
		return nil, nil, fmt.Errorf("recorded nothing: the game drew no frames")
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

	if !in.NoVideo {
		video, err := s.saveVideo(frames, in.Format)
		if err != nil {
			note += "\nno video: " + err.Error()
		} else {
			out["video"] = video
			note += "\nvideo: " + video.Path
			if video.URL != "" {
				note += "\n" + video.URL
			}
		}
	}

	// The animation itself, for a client that plays it. Where one does, it beats
	// a grid of stills at showing motion; where one does not, it shows a single
	// frame and the contact sheet is the better answer — which is why this is a
	// choice rather than a default.
	if in.Inline == "gif" {
		if video, ok := out["video"].(*Artifact); ok && video.Kind == "gif" {
			data, err := os.ReadFile(video.Path)
			if err == nil {
				return &mcpsdk.CallToolResult{
					Content: []mcpsdk.Content{
						&mcpsdk.ImageContent{Data: data, MIMEType: "image/gif"},
						&mcpsdk.TextContent{Text: note + "\n" + sheetArt.Path},
					},
				}, out, nil
			}
			note += "\ncould not read the gif back: " + err.Error()
		} else {
			note += "\nno gif was produced, so the contact sheet is what came back"
		}
	}

	data, err := encodePNG(fitInline(sheet, inlineMaxSize))
	if err != nil {
		return nil, nil, err
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.ImageContent{Data: data, MIMEType: "image/png"},
			&mcpsdk.TextContent{Text: note + "\n" + sheetArt.Path},
		},
	}, out, nil
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
	WaitTicks int    `json:"wait_ticks,omitempty" jsonschema:"ticks to let pass between the two captures; defaults to 30"`
	Stage     string `json:"stage,omitempty" jsonschema:"which drawing step to read: final for what the player sees, offscreen for what the game's own Draw produced before its DrawFinalScreen ran. Only differs for a game that draws its own final screen. Defaults to final"`
}

func (s *Server) compare(ctx context.Context, _ *mcpsdk.CallToolRequest, in compareInput) (*mcpsdk.CallToolResult, any, error) {
	stage, err := s.stage(in.Stage)
	if err != nil {
		return nil, nil, err
	}

	if in.WaitTicks <= 0 {
		in.WaitTicks = 30
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(in.WaitTicks)*100*time.Millisecond+defaultToolTimeout)
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

	data, err := encodePNG(fitInline(comparison, inlineMaxSize))
	if err != nil {
		return nil, nil, err
	}

	total := before.Image.Bounds().Dx() * before.Image.Bounds().Dy()
	note := fmt.Sprintf("tick %d vs %d: %d of %d pixels differ (%.2f%%)",
		before.Tick, after.Tick, changed, total, 100*float64(changed)/float64(total))

	return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.ImageContent{Data: data, MIMEType: "image/png"},
				&mcpsdk.TextContent{Text: note + "\n" + art.Path},
			},
		}, map[string]any{
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
