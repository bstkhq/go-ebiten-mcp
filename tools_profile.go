package ebitenmcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addProfileTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "game_profile",
		Description: "Where the time or the memory is going. game_frametimes says how much a " +
			"tick costs; this says what inside it. Returns the profile summarised as text, and " +
			"writes the pprof file alongside for anyone who wants to open it properly.",
		Annotations: readOnly("Profile"),
	}, s.profile)
}

type profileInput struct {
	Kind    string `json:"kind,omitempty" jsonschema:"cpu for where the time goes, heap for what is holding memory now, allocs for what has been allocating, goroutine for what everything is blocked on; defaults to cpu"`
	Seconds int    `json:"seconds,omitempty" jsonschema:"how long to sample for a cpu profile; defaults to 5"`
	Lines   int    `json:"lines,omitempty" jsonschema:"how many entries of the summary to return; defaults to 25"`
	Full    bool   `json:"full,omitempty" jsonschema:"summarise by call graph rather than by function, which is longer and shows who called what"`
}

func (s *Server) profile(ctx context.Context, _ *mcpsdk.CallToolRequest, in profileInput) (*mcpsdk.CallToolResult, any, error) {
	if in.Kind == "" {
		in.Kind = "cpu"
	}
	if in.Seconds <= 0 {
		in.Seconds = 5
	}
	if in.Lines <= 0 {
		in.Lines = 25
	}

	data, err := s.collectProfile(ctx, in)
	if err != nil {
		return nil, nil, err
	}

	art, err := s.media.save("profile-"+in.Kind, "pprof", data, 0, 0)
	if err != nil {
		return nil, nil, err
	}

	out := map[string]any{
		"kind":     in.Kind,
		"artifact": art,
		"tick":     s.rt.Tick(),
	}

	// The summary is the point. A pprof file is an artifact for a person with
	// the right tool; twenty lines naming the functions is an answer.
	summary, err := summariseProfile(art.Path, in)
	if err != nil {
		out["note"] = "could not summarise it: " + err.Error() +
			". The file is still there: open it with `go tool pprof " + art.Path + "`"
		return nil, out, nil
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: summary + "\n\n" + art.Path}},
	}, out, nil
}

func (s *Server) collectProfile(ctx context.Context, in profileInput) ([]byte, error) {
	var buf bytes.Buffer

	switch in.Kind {
	case "cpu":
		if err := pprof.StartCPUProfile(&buf); err != nil {
			return nil, fmt.Errorf("starting the cpu profile: %w "+
				"(only one can run at a time, so something else may be profiling)", err)
		}

		// Sampling has to happen while the game runs, so wait rather than
		// stepping: a paused game profiles as an idle one.
		select {
		case <-time.After(time.Duration(in.Seconds) * time.Second):
		case <-ctx.Done():
		}
		pprof.StopCPUProfile()

	case "heap", "allocs", "goroutine", "block", "mutex", "threadcreate":
		// A heap profile reflects the last GC unless one is forced, so a game
		// that just freed a lot would look like it still held it.
		if in.Kind == "heap" {
			runtime.GC()
		}

		profile := pprof.Lookup(in.Kind)
		if profile == nil {
			return nil, fmt.Errorf("no %s profile is being collected", in.Kind)
		}
		if err := profile.WriteTo(&buf, 0); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unknown profile %q: cpu, heap, allocs or goroutine", in.Kind)
	}

	return buf.Bytes(), nil
}

// summariseProfile runs the Go toolchain over the file.
//
// It needs the binary as well as the profile, since a pprof file carries
// addresses rather than names.
func summariseProfile(path string, in profileInput) (string, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return "", fmt.Errorf("no Go toolchain here to read it with")
	}

	binary, err := os.Executable()
	if err != nil {
		return "", err
	}

	mode := "-top"
	if in.Full {
		mode = "-traces"
	}

	cmd := exec.Command("go", "tool", "pprof", mode, "-nodecount", fmt.Sprint(in.Lines),
		binary, path)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}
