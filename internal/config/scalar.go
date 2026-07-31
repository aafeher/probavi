package config

import (
	"fmt"
	"strconv"
	"time"
)

// Duration is a positive time.Duration parsed from a YAML string like
// "30m" or "24h".
type Duration time.Duration

// UnmarshalYAML implements yaml.InterfaceUnmarshaler.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf(`a duration must be a string like "30m" or "24h"`)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf(`invalid duration %q (use Go syntax, e.g. "90s", "30m", "24h")`, s)
	}
	if v <= 0 {
		return fmt.Errorf("duration %q must be positive", s)
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Scalar is a YAML scalar (string, boolean, or integer) normalized to its
// canonical string form — the value a sql check compares against the
// sql_runner output. Floats are rejected: their textual round-trip is
// ambiguous, which is exactly what an assertion must not be.
type Scalar struct {
	set   bool
	value string
}

// UnmarshalYAML implements yaml.InterfaceUnmarshaler.
func (s *Scalar) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	switch t := v.(type) {
	case string:
		s.value = t
	case bool:
		s.value = strconv.FormatBool(t)
	case int:
		s.value = strconv.Itoa(t)
	case int64:
		s.value = strconv.FormatInt(t, 10)
	case uint64:
		s.value = strconv.FormatUint(t, 10)
	default:
		return fmt.Errorf("expect must be a string, boolean, or integer, got %T", v)
	}
	s.set = true
	return nil
}

// ScalarFromString constructs a set Scalar programmatically (tests, tools);
// YAML input goes through UnmarshalYAML instead.
func ScalarFromString(v string) Scalar { return Scalar{set: true, value: v} }

// IsSet reports whether a value was present in the YAML.
func (s Scalar) IsSet() bool { return s.set }

// String returns the normalized textual form.
func (s Scalar) String() string { return s.value }
