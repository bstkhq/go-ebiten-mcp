package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// WSL has no render node, only /dev/dxg. Without d3d12 and WSL's libraries the
// compositor and the game both drew on llvmpipe and every --gpu run was
// refused, on a machine with a GPU in it.
func TestWSLSelectsD3D12AndPutsItsLibrariesFirst(t *testing.T) {
	dir := t.TempDir()
	device := filepath.Join(dir, "dxg")
	libraries := filepath.Join(dir, "lib")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(libraries, 0o755); err != nil {
		t.Fatal(err)
	}
	swapGPUPaths(t, filepath.Join(dir, "no-render-node"), device, libraries)

	env := gpuEnvironment([]string{"LD_LIBRARY_PATH=/opt/game/lib", "HOME=/home/x"})

	if got, _ := lookupEnvironment(env, "GALLIUM_DRIVER"); got != "d3d12" {
		t.Errorf("GALLIUM_DRIVER = %q, want d3d12", got)
	}
	if got, _ := lookupEnvironment(env, "LD_LIBRARY_PATH"); got != libraries+":/opt/game/lib" {
		t.Errorf("LD_LIBRARY_PATH = %q, want WSL's libraries first and the game's kept", got)
	}
	if !slices.Contains(env, "HOME=/home/x") {
		t.Errorf("the rest of the environment was dropped: %q", env)
	}
}

// A driver somebody chose is theirs. If it draws in software the renderer check
// says so; overriding it would hide what they asked for.
func TestWSLLeavesADriverChosenByHand(t *testing.T) {
	env := wslGPUEnvironment([]string{"GALLIUM_DRIVER=llvmpipe"}, "/usr/lib/wsl/lib")

	if got, _ := lookupEnvironment(env, "GALLIUM_DRIVER"); got != "llvmpipe" {
		t.Errorf("GALLIUM_DRIVER = %q, want the llvmpipe that was asked for", got)
	}
}

// Present is not enough: a directory ahead of WSL's with a libGL of its own
// wins the loader's search, so WSL's comes first, once, and the rest keep
// their order.
func TestWSLLibrariesComeFirstOnceWhereverTheyWere(t *testing.T) {
	for _, path := range []string{
		"/opt/game/lib:/usr/lib/wsl/lib:/usr/local/lib",
		"/opt/game/lib:/usr/lib/wsl/lib/:/usr/local/lib:/usr/lib/wsl/lib",
		"/usr/lib/wsl/lib:/opt/game/lib:/usr/local/lib",
	} {
		env := wslGPUEnvironment([]string{"LD_LIBRARY_PATH=" + path}, "/usr/lib/wsl/lib")
		if got, _ := lookupEnvironment(env, "LD_LIBRARY_PATH"); got != "/usr/lib/wsl/lib:/opt/game/lib:/usr/local/lib" {
			t.Errorf("LD_LIBRARY_PATH from %q = %q, want WSL's first and the rest in order", path, got)
		}
	}
}

// A render node is the native GPU and comes first: WSL's variables on a machine
// with one would point Mesa at a driver it does not have.
func TestARenderNodeWinsOverWSL(t *testing.T) {
	dir := t.TempDir()
	vendor := filepath.Join(dir, "vendor")
	device := filepath.Join(dir, "dxg")
	for _, path := range []string{device} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(vendor, []byte(nvidiaRenderVendor+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapGPUPaths(t, vendor, device, dir)

	env := gpuEnvironment(nil)

	if _, set := lookupEnvironment(env, "GALLIUM_DRIVER"); set {
		t.Errorf("GALLIUM_DRIVER was set beside a render node: %q", env)
	}
	if got, _ := lookupEnvironment(env, "__GLX_VENDOR_LIBRARY_NAME"); got != "nvidia" {
		t.Errorf("__GLX_VENDOR_LIBRARY_NAME = %q, want nvidia", got)
	}
}

// The commands are each project's, written with or without --gpu. A host whose
// owner forbids software rendering has to be able to say so once, for all of
// them, and an unknown value must not quietly mean "allowed".
func TestHostThatRequiresTheGPUMakesEveryDisplayAGPUOne(t *testing.T) {
	t.Setenv(GPUEnvName, "require")
	for _, asked := range []bool{false, true} {
		if got, err := hostGPU(asked); err != nil || !got {
			t.Errorf("hostGPU(%v) = %v, %v; want true", asked, got, err)
		}
	}

	opts, _, err := parseRunFlags([]string{"--", "go", "test", "./..."})
	if err != nil || !opts.gpu {
		t.Errorf("run without --gpu on such a host: gpu = %v, err = %v; want a GPU display", opts.gpu, err)
	}

	t.Setenv(GPUEnvName, "")
	if got, err := hostGPU(false); err != nil || got {
		t.Errorf("without the policy hostGPU(false) = %v, %v; want false", got, err)
	}

	t.Setenv(GPUEnvName, "yes")
	if _, err := hostGPU(false); err == nil {
		t.Error("an unknown policy was accepted")
	}
}

func swapGPUPaths(t *testing.T, vendor, device, libraries string) {
	t.Helper()
	oldVendor, oldDevice, oldLibraries := renderVendorPath, wslGPUDevice, wslLibraries
	renderVendorPath, wslGPUDevice, wslLibraries = vendor, device, libraries
	t.Cleanup(func() { renderVendorPath, wslGPUDevice, wslLibraries = oldVendor, oldDevice, oldLibraries })
}
