package main

import "testing"

// The four hand-written parsers this replaced disagreed with each other in ways
// nobody chose, so these are about the behaviours that are now the same
// everywhere.
func TestParseFlagsStopsWhereItShould(t *testing.T) {
	specs := func() map[string]flagSpec {
		return map[string]flagSpec{
			"--gpu":    boolFlag(func() {}),
			"--screen": stringFlag(func(string) {}),
		}
	}

	for _, tc := range []struct {
		name string
		args []string
		rest []string
	}{
		{"nothing", nil, nil},
		{"only flags", []string{"--gpu"}, nil},
		{"a value", []string{"--screen", "800x600"}, nil},
		{"stops at the first non-flag", []string{"--gpu", "go", "run", "."}, []string{"go", "run", "."}},

		// `--` is consumed, and everything after it is somebody else's, flags
		// included. Only one of the four parsers used to honour it, which is why
		// `ebitenmcp run -- go test -run X` had to be spelled carefully.
		{"double dash", []string{"--gpu", "--", "--screen", "x"}, []string{"--screen", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest, err := parseFlags(tc.args, specs())
			if err != nil {
				t.Fatalf("parsing %q: %v", tc.args, err)
			}
			if len(rest) != len(tc.rest) {
				t.Fatalf("parsing %q left %q, want %q", tc.args, rest, tc.rest)
			}
			for i := range rest {
				if rest[i] != tc.rest[i] {
					t.Fatalf("parsing %q left %q, want %q", tc.args, rest, tc.rest)
				}
			}
		})
	}
}

func TestParseFlagsRefusesWhatItCannotHonour(t *testing.T) {
	specs := map[string]flagSpec{
		"--gpu":    boolFlag(func() {}),
		"--screen": stringFlag(func(string) {}),
		"--x":      checkedFlag(func(v string) error { return setXMode(new(string), v) }),
	}

	for _, args := range [][]string{
		{"--gpuu"},          // a typo used to be a silent default in one of the four
		{"--screen"},        // a value that is not there
		{"--x", "nonsense"}, // validated in one of the four and not the others
	} {
		if _, err := parseFlags(args, specs); err == nil {
			t.Errorf("parsing %q was accepted", args)
		}
	}
}
