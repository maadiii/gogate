package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a [time.Duration] that decodes from a duration string in YAML:
// "15m", "1h", "1h30m", "900s".
//
// A bare number is rejected rather than interpreted, because "900" is
// ambiguous between seconds and milliseconds — the config has to say which unit
// it means. Bare numbers are also the one case yaml would happily decode into
// something, so the check has to happen here rather than in validate().
type Duration time.Duration

// UnmarshalYAML implements [yaml.Unmarshaler].
//
// Errors carry the line number because this runs during yaml.Unmarshal, before
// validate() gets a chance to name the field: a config with several TTLs would
// otherwise give no clue which one was wrong.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Tag != "!!str" {
		return fmt.Errorf(
			"line %d: must include a unit, e.g. 15m, 1h30m, 900s (got %s)",
			value.Line, value.Value,
		)
	}

	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", value.Line, err)
	}

	*d = Duration(parsed)

	return nil
}

// String returns the duration in Go's canonical form, e.g. "15m0s". It makes
// Duration satisfy [fmt.Stringer], so it prints readably in validation errors.
func (d Duration) String() string {
	return time.Duration(d).String()
}
