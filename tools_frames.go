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
	BudgetMB int `json:"budget_mb,omitempty" jsonschema:"how much memory the buffer may use; defaults to 64"`
	Every    int `json:"every,omitempty" jsonschema:"keep one frame out of every N drawn. Raise it to cover more time for the same memory, or when frames are being dropped"`
}

type framesInput struct {
	Enable  *ringEnableInput `json:"enable,omitempty" jsonschema:"start keeping frames, discarding anything kept before"`
	Disable bool             `json:"disable,omitempty" jsonschema:"stop keeping frames and let go of them"`
	Last    int              `json:"last,omitempty" jsonschema:"how many of the most recent kept frames to return; defaults to 12"`
	Columns int              `json:"columns,omitempty" jsonschema:"columns in the contact sheet; defaults to 4"`
}

func (s *Server) frames(_ context.Context, _ *mcpsdk.CallToolRequest, in framesInput) (*mcpsdk.CallToolResult, any, error) {
	ring := s.rt.Ring()

	switch {
	case in.Enable != nil:
		ring.Enable(in.Enable.BudgetMB<<20, in.Enable.Every)
		return nil, map[string]any{
			"ring": ring.Status(),
			"note": "keeping frames now; ask again without enable to get them",
		}, nil

	case in.Disable:
		ring.Disable()
		return nil, map[string]any{"ring": ring.Status()}, nil
	}

	if in.Last <= 0 {
		in.Last = 12
	}
	if in.Columns <= 0 {
		in.Columns = 4
	}

	kept := ring.Frames(in.Last)
	if len(kept) == 0 {
		status := ring.Status()
		if enabled, _ := status["enabled"].(bool); !enabled {
			return nil, nil, fmt.Errorf(`nothing is being kept; turn it on with {"enable": {}} ` +
				"and the frames from then on will be there when you need them")
		}
		return nil, nil, fmt.Errorf("nothing kept yet: the game has not drawn since it was enabled")
	}

	// Decoded back into pixels only now, at the moment somebody wants to look.
	frames := make([]*Frame, 0, len(kept))
	for _, k := range kept {
		img, err := k.decode()
		if err != nil {
			continue
		}
		frames = append(frames, &Frame{Tick: k.Tick, Time: k.Time, Image: img})
	}

	if len(frames) == 0 {
		return nil, nil, fmt.Errorf("the kept frames could not be decoded")
	}

	sheet := contactSheet(frames, in.Columns)

	art, err := s.media.savePNG("before", sheet)
	if err != nil {
		return nil, nil, err
	}

	data, err := encodePNG(fitInline(sheet, inlineMaxSize))
	if err != nil {
		return nil, nil, err
	}

	note := fmt.Sprintf("%d frames, ticks %d to %d",
		len(frames), frames[0].Tick, frames[len(frames)-1].Tick)

	return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.ImageContent{Data: data, MIMEType: "image/png"},
				&mcpsdk.TextContent{Text: note + "\n" + art.Path},
			},
		}, map[string]any{
			"frames":        len(frames),
			"first_tick":    frames[0].Tick,
			"last_tick":     frames[len(frames)-1].Tick,
			"contact_sheet": art,
			"ring":          ring.Status(),
		}, nil
}
