package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// DurationYAML is a time.Duration loaded from YAML duration strings (e.g. "30s", "1m30s").
type DurationYAML time.Duration

// D returns the value as time.Duration.
func (d DurationYAML) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *DurationYAML) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = DurationYAML(parsed)
	return nil
}
