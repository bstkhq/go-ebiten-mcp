// Package upstream records what this module assumes about Ebitengine's
// internals, and nothing else.
//
// It deliberately does not import ebiten. Importing it would open a GLFW window
// during package initialisation, so a test that merely wants to check a version
// string could not run without a display. Keeping the assumptions here means
// the cheapest check in the project is also the one that runs anywhere.
package upstream

// Version is the single Ebitengine version internal/hook mirrors.
//
// The range is this narrow on purpose: hook writes into ui.InputState through a
// struct that copies its layout, so a field added upstream would move every
// offset after it. Raising this means re-reading internal/ui/input.go and
// updating InputStateSHA256 along with it.
const Version = "v2.9.9"

// InputStateSHA256 is the SHA-256 of the `type InputState struct { ... }`
// declaration in internal/ui/input.go, whitespace included.
//
// TestInputStateLayout recomputes it from the module cache. When it fails, the
// mirror in internal/hook is stale and must be brought back in line before
// anything writes through it again.
const InputStateSHA256 = "74fedecbc900bd271ef9c868632a445af57d0069bf21e78a4a25908c89364040"

// Module is the import path the layout is read from.
const Module = "github.com/hajimehoshi/ebiten/v2"
