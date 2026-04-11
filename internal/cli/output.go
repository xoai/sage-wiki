package cli

import (
	"encoding/json"
	"fmt"
	"os"
)

// Response is the standard JSON envelope for all CLI commands.
type Response struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Code  int    `json:"code,omitempty"`
}

// FormatJSON returns a JSON string with the standard envelope.
func FormatJSON(ok bool, data any, errMsg string) string {
	resp := Response{OK: ok, Data: data, Error: errMsg}
	if !ok && errMsg != "" {
		resp.Code = 1
	}
	out, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"error":"marshal failed: %v"}`, err)
	}
	return string(out)
}

// Output dispatches to JSON or text formatting based on format flag.
func Output(format string, text string, ok bool, data any, errMsg string) string {
	if format == "json" {
		return FormatJSON(ok, data, errMsg)
	}
	return text
}

// PrintResult prints to stdout (JSON) or stderr (text errors).
func PrintResult(format string, text string, ok bool, data any, errMsg string) {
	if !ok && format != "json" {
		fmt.Fprintln(os.Stderr, errMsg)
		return
	}
	fmt.Println(Output(format, text, ok, data, errMsg))
}

// ExitCode returns the appropriate exit code.
func ExitCode(ok bool, partial bool) int {
	if ok && !partial {
		return 0
	}
	if partial {
		return 2
	}
	return 1
}
