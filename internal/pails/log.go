package pails

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Line is one line of a deploy's log. Level is "step", "ok", "error" or "".
type Line struct {
	Time  time.Time `json:"time"`
	Text  string    `json:"text"`
	Level string    `json:"level,omitempty"`
	// Source is the container that printed the line, in a pail's output.
	Source string `json:"source,omitempty"`
}

// Log is a deploy's log. It can be read while the deploy is still writing it.
type Log struct {
	mu    sync.Mutex
	lines []Line
	// gone counts lines dropped from the front of a log that keeps only
	// its latest; max is how many such a log keeps, or 0 for all.
	gone int
	max  int
	done bool
	wake chan struct{}
}

func newLog() *Log { return &Log{wake: make(chan struct{})} }

// newOutput is a log that never finishes and keeps only its latest lines:
// what a pail's containers print.
func newOutput() *Log { return &Log{wake: make(chan struct{}), max: outputLines} }

// outputLines is how much of a pail's output Pail keeps.
const outputLines = 2000

func (l *Log) add(level, format string, args ...any) {
	l.append(Line{Time: time.Now().UTC(), Text: fmt.Sprintf(format, args...), Level: level})
}

func (l *Log) append(line Line) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if l.max > 0 && len(l.lines) > l.max+l.max/4 {
		drop := len(l.lines) - l.max
		l.lines = append([]Line(nil), l.lines[drop:]...)
		l.gone += drop
	}
	l.notify()
}

func (l *Log) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = true
	l.notify()
}

func (l *Log) notify() {
	close(l.wake)
	l.wake = make(chan struct{})
}

// Since returns the lines after the first n, how many lines there have been
// (the n to ask with next), whether the log is finished, and a channel that
// closes when there is more to read.
func (l *Log) Since(n int) (lines []Line, next int, done bool, wake <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at := max(n-l.gone, 0); at < len(l.lines) {
		lines = append(lines, l.lines[at:]...)
	}
	return lines, l.gone + len(l.lines), l.done, l.wake
}

func (l *Log) encode() []byte {
	lines, _, _, _ := l.Since(0)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, line := range lines {
		enc.Encode(line)
	}
	return buf.Bytes()
}

func decodeLog(b []byte) *Log {
	l := newLog()
	l.done = true
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var line Line
		if dec.Decode(&line) != nil {
			return l
		}
		l.lines = append(l.lines, line)
	}
}
