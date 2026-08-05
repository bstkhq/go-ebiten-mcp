package main

import "testing"

// The screen size reaches a shell inside the container, and x_start takes it
// from an MCP call, so anything that is not two numbers has to be refused here.
func TestSplitScreenRefusesAnythingButTwoNumbers(t *testing.T) {
	if w, h, err := splitScreen("1280x720"); err != nil || w != 1280 || h != 720 {
		t.Errorf("splitScreen(1280x720) = %d,%d,%v", w, h, err)
	}

	for _, bad := range []string{
		"1280x720; touch /pwned",
		"1280x$(id)",
		"1280x`id`",
		"1280x720 --foo",
		"1280x",
		"x720",
		"1280",
		"-1x720",
		"0x720",
		"99999x720",
		"1280x720x480",
		"",
	} {
		if w, h, err := splitScreen(bad); err == nil {
			t.Errorf("splitScreen(%q) accepted it as %d,%d", bad, w, h)
		}
	}
}
