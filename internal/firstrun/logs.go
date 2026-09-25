package firstrun

import (
	"fmt"
	"io"
	"sync"
)

// Bounds of the server log held while setup questions are open.
const (
	heldLogLines = 1000
	heldLogBytes = 1 << 20
	heldLogLine  = 4096
)

// LogGate holds server log lines while the terminal asks setup questions,
// so a log line never lands inside a prompt or a hidden answer. Held lines
// are written between cards and when setup ends. The oldest lines are
// dropped beyond the bounds, and the number dropped is reported.
type LogGate struct {
	mu      sync.Mutex
	target  io.Writer
	holding bool
	lines   [][]byte
	size    int
	dropped int
}

// NewLogGate returns a gate that writes to target and holds nothing yet.
func NewLogGate(target io.Writer) *LogGate { return &LogGate{target: target} }

// Write writes one log entry, or holds it while the gate is holding.
func (gate *LogGate) Write(entry []byte) (int, error) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.holding {
		return gate.target.Write(entry)
	}
	line := append([]byte(nil), entry...)
	if len(line) > heldLogLine {
		line = append(line[:heldLogLine-4], " ...\n"...)
	}
	gate.lines = append(gate.lines, line)
	gate.size += len(line)
	for len(gate.lines) > heldLogLines || gate.size > heldLogBytes {
		gate.size -= len(gate.lines[0])
		gate.lines[0] = nil
		gate.lines = gate.lines[1:]
		gate.dropped++
	}
	return len(entry), nil
}

// Hold starts holding log lines.
func (gate *LogGate) Hold() {
	gate.mu.Lock()
	gate.holding = true
	gate.mu.Unlock()
}

// Flush writes the held lines and keeps holding.
func (gate *LogGate) Flush() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.flushLocked()
}

// Release writes the held lines and stops holding.
func (gate *LogGate) Release() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.flushLocked()
	gate.holding = false
}

func (gate *LogGate) flushLocked() {
	if gate.dropped > 0 {
		fmt.Fprintf(gate.target, "(%d earlier log lines were dropped while setup questions were open)\n", gate.dropped)
		gate.dropped = 0
	}
	for _, line := range gate.lines {
		_, _ = gate.target.Write(line)
	}
	gate.lines, gate.size = nil, 0
}
