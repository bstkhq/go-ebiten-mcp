package ebitenmcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addFrameTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_frames",
		Description: "The frames from before something happened. game_record captures what comes " +
			"next, which only helps with a problem you already know how to reproduce; this keeps " +
			"a rolling buffer so the frames leading up to a crash or a glitch are still there " +
			"when you think to ask. Off by default because keeping frames costs the game time " +
			"and memory: enable it, let it run, then ask.",
		Annotations: readOnly("Frames before"),
	}, s.frames)
}

type ringEnableInput struct {
	BudgetMB int    `json:"budget_mb,omitempty" jsonschema:"how much memory the buffer may use; defaults to 64"`
	Every    int    `json:"every,omitempty" jsonschema:"keep one frame out of every N drawn. Raise it to cover more time for the same memory, or when frames are being dropped"`
	Stage    string `json:"stage,omitempty" jsonschema:"which drawing step to read: final for what the player sees, offscreen for what the game's own Draw produced before its DrawFinalScreen ran. Only differs for a game that draws its own final screen. Defaults to offscreen here, unlike the other capture tools, because this one runs continuously and the final screen is the size of the window"`
}

type framesInput struct {
	Enable  *ringEnableInput `json:"enable,omitempty" jsonschema:"start keeping frames, discarding anything kept before"`
	Disable bool             `json:"disable,omitempty" jsonschema:"stop keeping frames and let go of them"`
	Last    int              `json:"last,omitempty" jsonschema:"how many of the most recent kept frames to return; defaults to 12"`
	Columns int              `json:"columns,omitempty" jsonschema:"columns in the contact sheet; defaults to 4"`
	MaxSize int              `json:"max_size,omitempty" jsonschema:"longest side of the inline contact sheet in pixels; defaults to 1024"`
}

// frames is three tools wearing one name: turn the buffer on, turn it off, and
// ask it what it has. They are one tool because that is how it reads to whoever
// is using it — enable, wait, ask — but each is its own function here, since the
// read path used to be the body of this one after a switch that returned, which
// is easy to mistake for something that always runs.
func (s *Server) frames(_ context.Context, _ *mcpsdk.CallToolRequest, in framesInput) (*mcpsdk.CallToolResult, FramesOutput, error) {
	ring := s.rt.Ring()

	switch {
	case in.Enable != nil:
		return s.enableFrames(ring, *in.Enable)
	case in.Disable:
		ring.Disable()
		return nil, FramesOutput{Ring: ring.Status()}, nil
	default:
		return s.readFrames(ring, in)
	}
}

func (s *Server) enableFrames(ring *frameRing, in ringEnableInput) (*mcpsdk.CallToolResult, FramesOutput, error) {
	stage := Stage(in.Stage)
	if stage == "" {
		stage = StageOffscreen
	}
	if stage != StageOffscreen && stage != StageFinal {
		return nil, FramesOutput{}, fmt.Errorf("unknown stage %q: %s", in.Stage, stageDoc)
	}

	ring.Enable(in.BudgetMB<<20, in.Every, stage)

	return nil, FramesOutput{
		Ring: ring.Status(),
		Note: "keeping frames now; ask again without enable to get them",
	}, nil
}

func (s *Server) readFrames(ring *frameRing, in framesInput) (*mcpsdk.CallToolResult, FramesOutput, error) {
	if in.Last <= 0 {
		in.Last = 12
	}
	if in.Columns <= 0 {
		in.Columns = 4
	}

	kept := ring.Frames(in.Last)
	if len(kept) == 0 {
		if !ring.Status().Enabled {
			return nil, FramesOutput{}, fmt.Errorf(`nothing is being kept; turn it on with {"enable": {}} ` +
				"and the frames from then on will be there when you need them")
		}
		return nil, FramesOutput{}, fmt.Errorf("nothing kept yet: the game has not drawn since it was enabled")
	}

	// Decoded back into pixels only now, at the moment somebody wants to look.
	frames := make([]*Frame, 0, len(kept))
	for _, k := range kept {
		img, err := k.decode()
		if err != nil {
			continue
		}
		frames = append(frames, &Frame{Tick: k.Tick, Time: k.Time, Stage: ring.Stage(), Image: img})
	}

	if len(frames) == 0 {
		return nil, FramesOutput{}, fmt.Errorf("the kept frames could not be decoded")
	}

	sheet := contactSheet(frames, in.Columns)

	art, err := s.media.savePNG("before", sheet)
	if err != nil {
		return nil, FramesOutput{}, err
	}

	note := fmt.Sprintf("%d frames, ticks %d to %d",
		len(frames), frames[0].Tick, frames[len(frames)-1].Tick)

	result, _, err := imageResult(sheet, art, note, in.MaxSize, false)
	if err != nil {
		return nil, FramesOutput{}, err
	}

	return result, FramesOutput{
		Frames:    len(frames),
		FirstTick: frames[0].Tick,
		LastTick:  frames[len(frames)-1].Tick,
		Sheet:     art,
		Ring:      ring.Status(),
	}, nil
}
