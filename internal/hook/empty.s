// This file is deliberately empty.
//
// A //go:linkname declaration without a function body is only accepted in a
// package that contains an assembly file. Nothing here is ever assembled; the
// file exists so hook.go can declare the four Ebitengine internals it pulls in.
