package config

import (
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from YAML strings like "15m"
type Duration time.Duration

// UnmarshalYAML parses a Go duration string
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// Duration returns the value as a time.Duration
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}
