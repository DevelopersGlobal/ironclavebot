package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// StdioTransport implements MCP Transport over stdin/stdout of a subprocess
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	// writeSem allows one writer at a time while letting waiters give up when
	// their context ends.
	writeSem chan struct{}

	pendingMu sync.Mutex
	pending   map[string]chan *JSONRPCResponse
	dead      bool // reader ended; guarded by pendingMu

	closed atomic.Bool
}

// NewStdioTransport starts an external command and creates a stdio MCP transport
func NewStdioTransport(command string, args []string, env []string, dir string) (*StdioTransport, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if dir != "" {
		cmd.Dir = dir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("failed to start MCP process %q: %w", command, err)
	}

	t := &StdioTransport{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		writeSem: make(chan struct{}, 1),
		pending:  make(map[string]chan *JSONRPCResponse),
	}

	go t.readLoop()
	go t.stderrLoop()

	return t, nil
}

// writeLine writes one newline-terminated message. It gives up when ctx ends:
// a write that is stuck on a full pipe cannot be interrupted, so in that case
// the transport is closed (which kills the subprocess and unblocks the write),
// because a partial write would corrupt the framing anyway.
func (t *StdioTransport) writeLine(ctx context.Context, payload []byte) error {
	data := append(append([]byte{}, payload...), '\n')

	select {
	case t.writeSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	res := make(chan error, 1)
	go func() {
		_, err := t.stdin.Write(data)
		<-t.writeSem
		res <- err
	}()

	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		_ = t.Close()
		return ctx.Err()
	}
}

func (t *StdioTransport) readLoop() {
	scanner := bufio.NewScanner(t.stdout)
	// Allow large tool outputs (up to 16MB)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg incomingMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch {
		case msg.isServerRequest():
			// Answer on its own goroutine so a slow write cannot stall reading.
			go t.answerServerRequest(&msg)
		case msg.isServerNotification():
			// Notifications need no reply and are not tracked.
		case msg.ID != nil:
			key := idKey(msg.ID)
			t.pendingMu.Lock()
			ch, ok := t.pending[key]
			if ok {
				delete(t.pending, key)
			}
			t.pendingMu.Unlock()
			if ok && ch != nil {
				ch <- msg.response()
			}
		}
	}

	// Reader ended: mark the transport dead and release everyone waiting,
	// under the same lock Send uses to register, so no request can slip in
	// after the drain.
	t.pendingMu.Lock()
	t.dead = true
	for k, ch := range t.pending {
		close(ch)
		delete(t.pending, k)
	}
	t.pendingMu.Unlock()
}

func (t *StdioTransport) answerServerRequest(msg *incomingMessage) {
	data, err := json.Marshal(replyToServerRequest(msg))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = t.writeLine(ctx, data)
}

func (t *StdioTransport) stderrLoop() {
	// Drain without line tokenization so one very long line cannot stop the
	// drain and block the subprocess on a full pipe.
	_, _ = io.Copy(io.Discard, t.stderr)
}

func (t *StdioTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("stdio transport is closed")
	}

	idKeyStr := idKey(req.ID)
	ch := make(chan *JSONRPCResponse, 1)

	t.pendingMu.Lock()
	if t.dead {
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("stdio transport is closed: MCP process output ended")
	}
	t.pending[idKeyStr] = ch
	t.pendingMu.Unlock()

	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, idKeyStr)
		t.pendingMu.Unlock()
	}()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}
	if err := t.writeLine(ctx, data); err != nil {
		return nil, fmt.Errorf("failed to write to MCP stdin: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, fmt.Errorf("transport closed or nil response received")
		}
		return resp, nil
	}
}

func (t *StdioTransport) Notify(ctx context.Context, req *JSONRPCRequest) error {
	if t.closed.Load() {
		return fmt.Errorf("stdio transport is closed")
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON-RPC notification: %w", err)
	}
	return t.writeLine(ctx, data)
}

func (t *StdioTransport) Close() error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}

	_ = t.stdin.Close()
	_ = t.stdout.Close()
	_ = t.stderr.Close()

	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
	}

	return nil
}
