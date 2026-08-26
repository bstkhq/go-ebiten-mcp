package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPodmanKeepsTheHostRenderGroupForGPU(t *testing.T) {
	got := strings.Join(gpuContainerArgs("/usr/bin/podman"), " ")
	if !strings.Contains(got, "--device "+renderNode) {
		t.Errorf("podman arguments omit the render node: %s", got)
	}
	if !strings.Contains(got, "--group-add keep-groups") {
		t.Errorf("podman arguments drop the host render group: %s", got)
	}
	global := strings.Join(gpuContainerGlobalArgs("/usr/bin/podman"), " ")
	if global != "--runtime crun" {
		t.Errorf("podman was not pinned to the runtime that supports keep-groups: %s", global)
	}

	docker := strings.Join(gpuContainerArgs("/usr/bin/docker"), " ")
	if strings.Contains(docker, "keep-groups") {
		t.Errorf("docker was given podman-only arguments: %s", docker)
	}
	if global := gpuContainerGlobalArgs("/usr/bin/docker"); len(global) != 0 {
		t.Errorf("docker was given podman-only global arguments: %q", global)
	}
}

// A name can be reused immediately after a server exits. Cleanup must compare
// the inode it saw while its own server was alive, or it can unlink the next
// server's socket in that narrow window.
func TestDisplayCleanupLeavesAReplacementArtifactAlone(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original")
	replaced := filepath.Join(dir, "replaced")

	for _, path := range []string{original, replaced} {
		if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	originalInfo, err := os.Lstat(original)
	if err != nil {
		t.Fatal(err)
	}
	replacedInfo, err := os.Lstat(replaced)
	if err != nil {
		t.Fatal(err)
	}
	past := replacedInfo.ModTime().Add(-time.Hour)
	if err := os.Chtimes(replaced, past, past); err != nil {
		t.Fatal(err)
	}
	replacedInfo, err = os.Lstat(replaced)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := displayArtifacts{
		{path: original, info: originalInfo},
		{path: replaced, info: replacedInfo},
	}

	if err := os.Remove(replaced); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replaced, []byte("next server"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := artifacts.remove(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Lstat(original); !os.IsNotExist(err) {
		t.Errorf("the original artifact remains (stat error %v)", err)
	}
	if got, err := os.ReadFile(replaced); err != nil || string(got) != "next server" {
		t.Errorf("the replacement was changed: content %q, error %v", got, err)
	}
}

func TestDisplayNumberCannotNameAnotherPath(t *testing.T) {
	for _, display := range []string{"", "1", ":-1", ":1.0", ":01", ":../../other"} {
		if _, err := displayNumber(display); err == nil {
			t.Errorf("accepted display %q", display)
		}
	}
	if n, err := displayNumber(":8"); err != nil || n != 8 {
		t.Errorf("display :8 parsed as %d, %v", n, err)
	}
}
