// report.go is the result type and its text and JSON output.
//
// Works with: doctor.go and live.go, which append Checks.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/antidamage/subslip/internal/redact"
)

// Status of one check.
type Status string

const (
	Pass Status = "PASS"
	Warn Status = "WARN"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
	Info Status = "INFO"
)

// Check is one line of the report.
type Check struct {
	Status Status `json:"status"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Failed reports whether any check failed.
func Failed(checks []Check) bool {
	for _, c := range checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// WriteText prints one check per line with its hint indented below.
func WriteText(w io.Writer, checks []Check) {
	for _, c := range checks {
		fmt.Fprintf(w, "[%s] %s: %s\n", c.Status, c.Name, redact.String(c.Detail))
		if c.Hint != "" {
			fmt.Fprintf(w, "       %s\n", redact.String(c.Hint))
		}
	}
}

// WriteJSON prints the checks as a JSON array, for agents.
func WriteJSON(w io.Writer, checks []Check) error {
	clean := make([]Check, len(checks))
	for i, c := range checks {
		c.Detail, c.Hint = redact.String(c.Detail), redact.String(c.Hint)
		clean[i] = c
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"checks": clean, "failed": Failed(checks)})
}
