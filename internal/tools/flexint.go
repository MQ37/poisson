package tools

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// FlexInt unmarshals from either a JSON number or a numeric JSON string —
// models occasionally send integer parameters as strings (e.g. "12" instead
// of 12). A value that isn't a plain integer either way (e.g. a range like
// "80, 220") is rejected with a clear, actionable error instead of Go's raw
// "json: cannot unmarshal string into Go struct field ... of type int",
// which gives the model nothing to correct — same philosophy edit.go's
// parseEditInput already applies to its own inputs.
type FlexInt int

func (f *FlexInt) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*f = FlexInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("expected a single integer, got %s", data)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("expected a single integer, got %q — pass one number per field, not a range or list", s)
	}
	*f = FlexInt(n)
	return nil
}
