package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"
)

// tw returns a tabwriter writing to stdout, used for aligned table output.
func tw() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
}

// ageStr renders a duration-since-t as a compact human string (e.g. "5m", "3h").
func ageStr(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// stateFromJSON pulls a human-readable state out of a raw JSON state blob,
// trying common keys; falls back to the raw string or "unknown".
func stateFromJSON(raw string) string {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	for _, k := range []string{"state", "status", "power_state"} {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return "unknown"
}
