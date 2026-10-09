package actions

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStepDisplay(t *testing.T) {
	for _, test := range []struct {
		name                  string
		step                  Step
		wantName, wantCommand string
	}{
		{"single line", Step{Run: "echo ok"}, "Run echo ok", "echo ok"},
		{"multi-line", Step{Run: "echo first\necho next\n"}, "Run echo first …", "echo first …"},
		{"blank lines", Step{Run: "\n \necho ok\n \n"}, "Run echo ok", "echo ok"},
		{"empty", Step{Run: " \n\r"}, "Run run", "run"},
		{"uses", Step{Uses: "actions/checkout@v4"}, "actions/checkout@v4", "actions/checkout@v4"},
		{"named uses", Step{Name: " Source ", Uses: "actions/checkout@v4"}, "Source", "actions/checkout@v4"},
		{"CR", Step{Name: "First\r\nNext", Run: "echo first\recho next"}, "First  Next", "echo first …"},
		{"long command", Step{Name: "Long", Run: strings.Repeat("x", 600)}, "Long", strings.Repeat("x", 508) + " …"},
		{"long UTF-8 name", Step{Name: strings.Repeat("가", 80), Run: "echo ok"}, strings.Repeat("가", 32) + " …", "echo ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			name, command := StepDisplay(test.step)
			if name != test.wantName || command != test.wantCommand || !utf8.ValidString(name) || !utf8.ValidString(command) || strings.ContainsAny(name+command, "\r\n") {
				t.Fatalf("name=%q command=%q, want %q %q", name, command, test.wantName, test.wantCommand)
			}
		})
	}
}
