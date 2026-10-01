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
}

// Log is a deploy's log. It can be read while the deploy is still writing it.
type Log struct {
	mu    sync.Mutex
	lines []Line
	done  bool
	wake  chan struct{}
}

func newLog() *Log { return &Log{wake: make(chan struct{})} }

func (l *Log) add(level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, Line{Time: time.Now().UTC(), Text: fmt.Sprintf(format, args...), Level: level})
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

// Since returns the lines after the first n, whether the log is finished,
// and a channel that closes when there is more to read.
func (l *Log) Since(n int) (lines []Line, done bool, wake <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n < len(l.lines) {
		lines = append(lines, l.lines[n:]...)
	}
	return lines, l.done, l.wake
}

func (l *Log) encode() []byte {
	lines, _, _ := l.Since(0)
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
