package main

import (
	"fmt"
	"strings"
)

// One reader for the flags, because there were four.
//
// They were the same loop written four times with four sets of differences: one
// stopped at `--` and the others did not, one required two arguments to be left
// and so silently ignored a trailing flag, one accepted an unknown flag and
// three refused it. None of that was on purpose — it is what a hand-written
// parser looks like after it has been copied three times — and the differences
// were invisible until somebody hit one.
//
// Not flag.FlagSet, deliberately. This has to stop at `--` and hand the rest
// over untouched, since everything after it is the command to run; a FlagSet
// would need the same wrapper around it and a package-level side effect besides.

// flagSpec is one flag: whether it takes a value, and what to do with it.
type flagSpec struct {
	// value is nil for a flag that is just present, like --gpu.
	value func(string) error
	set   func()
}

// boolFlag is a flag with no value.
func boolFlag(set func()) flagSpec { return flagSpec{set: set} }

// stringFlag takes a value.
func stringFlag(set func(string)) flagSpec {
	return flagSpec{value: func(v string) error { set(v); return nil }}
}

// checkedFlag takes a value and may refuse it.
func checkedFlag(set func(string) error) flagSpec { return flagSpec{value: set} }

// parseFlags reads leading flags and returns what is left.
//
// It stops at the first argument that is not a flag, or at `--`, which it
// consumes. An unknown flag is an error everywhere now: guessing at what
// somebody meant by --gpuu is how a typo becomes a silent default.
func parseFlags(args []string, specs map[string]flagSpec) ([]string, error) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		name := args[0]
		args = args[1:]

		if name == "--" {
			return args, nil
		}

		spec, ok := specs[name]
		if !ok {
			return nil, fmt.Errorf("unknown flag %q", name)
		}

		if spec.value == nil {
			spec.set()
			continue
		}

		if len(args) == 0 {
			return nil, fmt.Errorf("%s needs a value", name)
		}

		if err := spec.value(args[0]); err != nil {
			return nil, err
		}
		args = args[1:]
	}

	return args, nil
}
