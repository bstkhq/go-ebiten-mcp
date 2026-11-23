package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bstkhq/go-ebiten-mcp/internal/wire"
)

// GPUEnvName is the host's rendering policy. Set to "require", nothing here
// draws in software: every display is a GPU one, as though --gpu had been
// given, so a run that would have quietly landed on Xvfb or pixman either gets
// the GPU or fails saying why.
//
// A host setting rather than a flag because the flag is in every project's
// commands, written by whoever wrote them, and a machine whose owner decided
// that software rendering produces nothing worth having cannot go and edit
// them all.
const (
	GPUEnvName  = wire.GPUEnv
	gpuRequired = "require"
)

// hostGPU is whether a display has to render on the GPU: because the command
// asked for it, or because the host requires it of every display.
func hostGPU(asked bool) (bool, error) {
	switch v := os.Getenv(GPUEnvName); v {
	case "":
		return asked, nil
	case gpuRequired:
		return true, nil
	default:
		return false, fmt.Errorf("%s takes %q or nothing, not %q", GPUEnvName, gpuRequired, v)
	}
}

// WSL has no render node. Its GPU is /dev/dxg, which Mesa reaches through its
// d3d12 Gallium driver and the libraries WSL mounts in /usr/lib/wsl/lib; with
// neither set, weston and the game both land on llvmpipe and --gpu refuses the
// run. Variables so that a test can point them somewhere it made.
var (
	renderVendorPath = renderNodeVendorPath
	wslGPUDevice     = "/dev/dxg"
	wslLibraries     = "/usr/lib/wsl/lib"
)

const nvidiaRenderVendor = "0x10de"

// gpuEnvironment is what the compositor, the game and glxinfo need to find the
// GPU rather than a software rasteriser.
func gpuEnvironment(env []string) []string {
	if vendor, err := os.ReadFile(renderVendorPath); err == nil {
		return gpuEnvironmentForVendor(env, strings.TrimSpace(string(vendor)))
	}
	if _, err := os.Stat(wslGPUDevice); err != nil {
		return env
	}
	if info, err := os.Stat(wslLibraries); err != nil || !info.IsDir() {
		return env
	}
	return wslGPUEnvironment(env, wslLibraries)
}

func gpuEnvironmentForVendor(env []string, vendor string) []string {
	if vendor != nvidiaRenderVendor {
		return env
	}

	// NVIDIA's GLVND client does not select its GLX implementation through
	// Xwayland on every distribution. Without these the compositor itself uses
	// NVIDIA while the game silently lands on llvmpipe.
	env = setEnvironment(env, "__NV_PRIME_RENDER_OFFLOAD", "1")
	return setEnvironment(env, "__GLX_VENDOR_LIBRARY_NAME", "nvidia")
}

// wslGPUEnvironment selects d3d12 unless somebody already chose a driver, and
// puts WSL's libraries first on the library path without dropping what was
// there. A driver chosen by hand is left alone: if it draws in software, the
// renderer check says so.
func wslGPUEnvironment(env []string, libraries string) []string {
	if _, set := lookupEnvironment(env, "GALLIUM_DRIVER"); !set {
		env = setEnvironment(env, "GALLIUM_DRIVER", "d3d12")
	}

	path, _ := lookupEnvironment(env, "LD_LIBRARY_PATH")
	for _, entry := range filepath.SplitList(path) {
		if filepath.Clean(entry) == filepath.Clean(libraries) {
			return env
		}
	}
	if path != "" {
		libraries += string(filepath.ListSeparator) + path
	}
	return setEnvironment(env, "LD_LIBRARY_PATH", libraries)
}

func lookupEnvironment(env []string, key string) (string, bool) {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], prefix); ok {
			return value, true
		}
	}
	return "", false
}
