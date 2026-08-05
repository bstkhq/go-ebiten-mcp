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

// maxProfileSeconds is long enough to catch anything periodic and short enough
// that a mistyped number is an error rather than an outage.
const maxProfileSeconds = 120

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
	// Capped, because a cpu profile holds the process-global profiler for its
	// whole duration: one that ran for a day would block this handler and make
	// every later game_profile fail with "only one can run at a time".
	if in.Seconds > maxProfileSeconds {
		return nil, nil, fmt.Errorf("a %ds profile would hold the profiler that long and block "+
			"every other one; the limit is %ds", in.Seconds, maxProfileSeconds)
	}
	if in.Lines <= 0 {
		in.Lines = 25
	}

	// The budget covers the sampling plus the toolchain run that summarises it.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(in.Seconds)*time.Second+2*time.Minute)
	defer cancel()

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
	summary, err := summariseProfile(ctx, art.Path, in)
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
func summariseProfile(ctx context.Context, path string, in profileInput) (string, error) {
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

	// Under the context: `go tool pprof` can fetch modules, and a handler that
	// waited for that would be waiting on somebody's network.
	cmd := exec.CommandContext(ctx, "go", "tool", "pprof", mode, "-nodecount", fmt.Sprint(in.Lines),
		binary, path)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}
