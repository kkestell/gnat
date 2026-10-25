package entry

import (
	"bytes"
	"strings"
	"testing"
)

func TestCommandSurface(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		status   int
		contains string
	}{
		{[]string{"--help"}, 0, "--no-open"},
		{[]string{"--version"}, 0, "v2-dev"},
		{[]string{"create", "demo"}, 1, "unknown command"},
		{[]string{"--unknown"}, 1, "unknown flag"},
		{[]string{"daemon", "stop", "extra"}, 1, "unknown command"},
	} {
		var out, errs bytes.Buffer
		status := Run(tc.args, &out, &errs)
		text := out.String() + errs.String()
		if status != tc.status || !strings.Contains(text, tc.contains) {
			t.Fatalf("%v: status %d: %s", tc.args, status, text)
		}
	}
}
