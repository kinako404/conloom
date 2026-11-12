package conloomapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration. It is encoded as a Go duration string such as
// "1.5s" and decodes either such a string or an integer number of milliseconds.
type Duration struct {
	time.Duration
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		return d.parseString(s)
	}
	return d.parseMillis(string(b))
}

func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration: line %d: expected scalar", n.Line)
	}
	if n.ShortTag() == "!!int" {
		return d.parseMillis(n.Value)
	}
	return d.parseString(n.Value)
}

func (d *Duration) parseString(s string) error {
	if s == "" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration: %w", err)
	}
	d.Duration = v
	return nil
}

func (d *Duration) parseMillis(s string) error {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("duration: %q is neither a duration string nor integer milliseconds", s)
	}
	d.Duration = time.Duration(ms) * time.Millisecond
	return nil
}
