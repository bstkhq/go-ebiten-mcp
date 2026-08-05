package ebitenmcp

import "testing"

// TestLoopbackDecidesWhoCanReachIt.
//
// There is no authentication anywhere in this package, so the address is the
// entire access control and getting this predicate wrong means either a warning
// nobody needs or, much worse, silence about a game the whole network can drive.
func TestLoopbackDecidesWhoCanReachIt(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8384", true},
		{"127.0.0.53:8384", true},
		{"[::1]:8384", true},
		{"localhost:8384", true},

		{"0.0.0.0:8384", false},
		{"[::]:8384", false},
		{":8384", false},
		{"192.168.1.10:8384", false},
		{"kiosk.local:8384", false},

		// Not an address at all. Warning about it is the safe way to be wrong.
		{"nonsense", false},
	} {
		if got := loopback(tc.addr); got != tc.want {
			t.Errorf("loopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
