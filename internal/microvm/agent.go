package microvm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// A function's copy runs an agent, which takes requests from the host and
// runs the function's program for each. What follows is how the two talk,
// over whatever connects them: a microVM's vsock, or a container's network.

// Frames from the agent: a kind, a length, and that many bytes.
const (
	frameStdout = 1
	frameStderr = 2
	frameResult = 3
)

// agentRequest is what the host asks of the agent in a function's copy.
type agentRequest struct {
	// Op is "hello" or "run".
	Op string `json:"op"`
	// Token says the request is the host's, where others can reach the agent.
	Token string `json:"token,omitempty"`
	// For hello: the guest's address on its network, and the time.
	IP      string `json:"ip,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	Time    int64  `json:"time,omitempty"`
	// For run.
	Argv      []string `json:"argv,omitempty"`
	Env       []string `json:"env,omitempty"`
	Dir       string   `json:"dir,omitempty"`
	BodyLen   int64    `json:"body_len,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
}

// agentResult is how the agent says a request ended.
type agentResult struct {
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
	OOM      bool   `json:"oom,omitempty"`
}

// AgentHello asks the agent at the other end of conn whether it is ready.
func AgentHello(ctx context.Context, conn net.Conn, token string) error {
	_, err := converse(ctx, conn, agentRequest{Op: "hello", Token: token}, nil, 0, nil, 0, 5*time.Second)
	return err
}

// AgentCall has the agent at the other end of conn run one program, and
// waits for it to end.
func AgentCall(ctx context.Context, conn net.Conn, token string, call FunctionCall) (FunctionResult, error) {
	req := agentRequest{
		Op: "run", Token: token, Argv: call.Argv, Env: call.Env, Dir: call.Dir,
		BodyLen: call.BodyLen, TimeoutMS: call.Timeout.Milliseconds(),
	}
	// The agent stops the program at its timeout; the extra is for a copy
	// that has stopped answering altogether.
	return converse(ctx, conn, req, call.Body, call.BodyLen, call.Stderr, call.MaxOutput, call.Timeout+5*time.Second)
}

// converse sends the agent one request over conn and reads its answer.
func converse(ctx context.Context, conn net.Conn, req agentRequest, body io.Reader, bodyLen int64, stderr func(string), maxOutput int64, limit time.Duration) (FunctionResult, error) {
	var res FunctionResult
	conn.SetDeadline(time.Now().Add(limit))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	header, _ := json.Marshal(req)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(header)))
	if _, err := conn.Write(append(size[:], header...)); err != nil {
		return res, err
	}
	if body != nil && bodyLen > 0 {
		// Sent while the answer is read: a program may write before it has
		// read everything it was sent.
		go io.CopyN(conn, body, bodyLen)
	}

	r := bufio.NewReader(conn)
	var out, errLine bytes.Buffer
	for {
		var head [5]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, fmt.Errorf("the function's copy stopped answering: %w", err)
		}
		n := int64(binary.BigEndian.Uint32(head[1:]))
		switch head[0] {
		case frameStdout:
			if maxOutput > 0 && int64(out.Len())+n > maxOutput {
				// Closing the connection has the agent stop the program.
				res.TooBig = true
				return res, nil
			}
			if _, err := io.CopyN(&out, r, n); err != nil {
				return res, err
			}
		case frameStderr:
			if _, err := io.CopyN(&errLine, r, n); err != nil {
				return res, err
			}
			for {
				line, rest, found := bytes.Cut(errLine.Bytes(), []byte("\n"))
				if !found {
					break
				}
				if stderr != nil {
					stderr(strings.TrimRight(string(line), "\r"))
				}
				errLine = *bytes.NewBuffer(append([]byte(nil), rest...))
			}
		case frameResult:
			var ar agentResult
			if err := json.NewDecoder(io.LimitReader(r, n)).Decode(&ar); err != nil {
				return res, err
			}
			if errLine.Len() > 0 && stderr != nil {
				stderr(errLine.String())
			}
			if ar.Error != "" {
				return res, errors.New(ar.Error)
			}
			res.Output, res.ExitCode, res.Signal = out.Bytes(), ar.ExitCode, ar.Signal
			res.TimedOut, res.OutOfMemory = ar.TimedOut, ar.OOM
			return res, nil
		default:
			return res, fmt.Errorf("the function's copy sent something Pail doesn't understand")
		}
	}
}
