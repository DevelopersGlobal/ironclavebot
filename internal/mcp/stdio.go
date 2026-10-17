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
)

// StdioTransport implements MCP Transport over stdin/stdout of a subprocess
type StdioTransport struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan *JSONRPCResponse
	closed    atomic.Bool
	done      chan struct{}
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
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		pending: make(map[string]chan *JSONRPCResponse),
		done:    make(chan struct{}),
	}

	go t.readLoop()
	go t.stderrLoop()

	return t, nil
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

		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}

		if resp.ID != nil {
			idKey := fmt.Sprint(resp.ID)
			t.pendingMu.Lock()
			ch, ok := t.pending[idKey]
			if ok {
				delete(t.pending, idKey)
			}
			t.pendingMu.Unlock()

			if ok && ch != nil {
				ch <- &resp
			}
		}
	}

	t.pendingMu.Lock()
	for k, ch := range t.pending {
		close(ch)
		delete(t.pending, k)
	}
	t.pendingMu.Unlock()
}

func (t *StdioTransport) stderrLoop() {
	scanner := bufio.NewScanner(t.stderr)
	for scanner.Scan() {
		// Log or suppress stderr from sub-agent
	}
}

func (t *StdioTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("stdio transport is closed")
	}

	idKey := fmt.Sprint(req.ID)
	ch := make(chan *JSONRPCResponse, 1)

	t.pendingMu.Lock()
	t.pending[idKey] = ch
	t.pendingMu.Unlock()

	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, idKey)
		t.pendingMu.Unlock()
	}()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}
	data = append(data, '\n')

	t.writeMu.Lock()
	_, err = t.stdin.Write(data)
	t.writeMu.Unlock()
	if err != nil {
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
	data = append(data, '\n')

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err = t.stdin.Write(data)
	return err
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
