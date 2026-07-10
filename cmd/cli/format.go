package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// colPad is the gap (in spaces) between table columns, matching the old tabwriter.
const colPad = 3

// ansiSeq matches an ANSI CSI colour sequence, e.g. "\033[1m" or "\033[0m".
var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

// visibleWidth is the on-screen rune width of s, ignoring ANSI colour codes.
// text/tabwriter counts the escape bytes as visible characters, which pushes
// bold headers and coloured status cells out of alignment; sizing columns on
// this instead keeps them aligned.
func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiSeq.ReplaceAllString(s, ""))
}

// table is a left-aligned column writer for CLI output. Callers write
// tab-separated, newline-terminated rows (as with text/tabwriter) and call
// Flush; the rows are then padded to the widest visible cell per column.
type table struct {
	buf  bytes.Buffer
	rows [][]string
}

func (t *table) Write(p []byte) (int, error) {
	t.buf.Write(p)
	for {
		line, err := t.buf.ReadString('\n')
		if err != nil {
			// no complete line yet; keep the remainder buffered
			t.buf.WriteString(line)
			break
		}
		t.rows = append(t.rows, strings.Split(strings.TrimRight(line, "\n"), "\t"))
	}
	return len(p), nil
}

func (t *table) Flush() error {
	var widths []int
	for _, row := range t.rows {
		for i, cell := range row {
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			if w := visibleWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var out strings.Builder
	for _, row := range t.rows {
		for i, cell := range row {
			out.WriteString(cell)
			if i < len(row)-1 {
				out.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(cell)+colPad))
			}
		}
		out.WriteByte('\n')
	}
	_, err := fmt.Fprint(os.Stdout, out.String())
	return err
}

// tw returns an aligned-column writer over stdout, used for table output.
func tw() *table {
	return &table{}
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
