package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

func isTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func colorize(color, s string) string {
	if !isTTY() {
		return s
	}
	return color + s + colorReset
}

func statusColor(status string) string {
	switch status {
	case "running", "healthy", "connected", "available":
		return colorize(colorGreen, status)
	case "failed", "terminated", "error":
		return colorize(colorRed, status)
	case "building", "deploying", "pending", "starting", "unknown":
		return colorize(colorYellow, status)
	case "destroyed", "stopped", "deallocated":
		return colorize(colorGray, status)
	default:
		return status
	}
}

func severityColor(s string) string {
	switch s {
	case "critical":
		return colorize(colorRed+colorBold, s)
	case "high":
		return colorize(colorRed, s)
	case "medium":
		return colorize(colorYellow, s)
	default:
		return colorize(colorGray, s)
	}
}

func bold(s string) string {
	return colorize(colorBold, s)
}

func success(msg string) {
	fmt.Println(colorize(colorGreen, "✓ ") + msg)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, colorize(colorRed, "✗ ")+msg)
	os.Exit(1)
}
