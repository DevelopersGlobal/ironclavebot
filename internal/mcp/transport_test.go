package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- stdio: helper process acting as an MCP server ----

func TestHelperMCPServer(t *testing.T) {
	mode := os.Getenv("MCP_HELPER_MODE")
	if mode == "" {
		return
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	out := bufio.NewWriter(os.Stdout)
	send := func(v string) { fmt.Fprintln(out, v); out.Flush() }

	switch mode {
	case "ping-same-id":
		// Read the client's request, then send a server request (ping) that
		// reuses the SAME id, expect the client to answer it, then respond.
		in.Scan()
		var req JSONRPCRequest
		_ = json.Unmarshal(in.Bytes(), &req)
		id := idKey(req.ID)
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"ping"}`, id))
		in.Scan() // the client's reply to our ping
		var reply map[string]interface{}
		_ = json.Unmarshal(in.Bytes(), &reply)
		if _, ok := reply["result"]; !ok {
			send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-1,"message":"client did not answer ping"}}`, id))
			return
		}
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`, id))
		time.Sleep(200 * time.Millisecond)
	case "exit-immediately":
		return
	case "huge-stderr":
		// One very long stderr line, then a normal reply.
		fmt.Fprint(os.Stderr, strings.Repeat("e", 300000)+"\n")
		in.Scan()
		var req JSONRPCRequest
		_ = json.Unmarshal(in.Bytes(), &req)
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`, idKey(req.ID)))
	}
}

func helperTransport(t *testing.T, mode string) *StdioTransport {
	tr, err := NewStdioTransport(os.Args[0], []string{"-test.run=TestHelperMCPServer"}, []string{"MCP_HELPER_MODE=" + mode}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func TestStdioServerRequestIsNotTreatedAsResponse(t *testing.T) {
	tr := helperTransport(t, "ping-same-id")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: "2.0", ID: int64(1), Method: "tools/list"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("server ping consumed or broke the pending request: %v", resp.Error)
	}
	var res map[string]bool
	_ = json.Unmarshal(resp.Result, &res)
	if !res["ok"] {
		t.Fatalf("real response not delivered, got %s", resp.Result)
	}
}

func TestStdioSendFailsFastAfterProcessExit(t *testing.T) {
	tr := helperTransport(t, "exit-immediately")
	time.Sleep(500 * time.Millisecond) // let the reader see EOF
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: "2.0", ID: int64(1), Method: "ping"})
	if err == nil {
		t.Fatal("expected an error after the process exited")
	}
	if time.Since(start) > time.Second {
		t.Errorf("Send waited %s instead of failing fast", time.Since(start))
	}
}

func TestStdioLongStderrLineDoesNotStall(t *testing.T) {
	tr := helperTransport(t, "huge-stderr")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: "2.0", ID: int64(7), Method: "x"}); err != nil {
		t.Fatalf("send failed: %v", err)
	}
}

func TestIDKeyNormalizesLargeIDs(t *testing.T) {
	// JSON decodes numbers as float64; 1,000,000 prints as "1e+06" with fmt.Sprint.
	var msg incomingMessage
	_ = json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1000000,"result":{}}`), &msg)
	if idKey(msg.ID) != idKey(int64(1000000)) {
		t.Errorf("id mismatch: %q vs %q", idKey(msg.ID), idKey(int64(1000000)))
	}
}

// ---- SSE ----

type sseFixture struct {
	srv      *httptest.Server
	mu       sync.Mutex
	posts    []string
	events   chan string // raw SSE text to write to the stream
	postCode int
	respond  func(req JSONRPCRequest, w http.ResponseWriter) bool
}

func newSSEFixture(t *testing.T) *sseFixture {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no local sockets:", err)
	}
	f := &sseFixture{events: make(chan string, 16), postCode: http.StatusAccepted}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl := w.(http.Flusher)
			fmt.Fprint(w, "event: endpoint\ndata: /messages\n\n")
			fl.Flush()
			for {
				select {
				case ev := <-f.events:
					if ev == "CLOSE" {
						return
					}
					fmt.Fprint(w, ev)
					fl.Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		rawBody, _ := io.ReadAll(r.Body)
		var req JSONRPCRequest
		_ = json.Unmarshal(rawBody, &req)
		f.mu.Lock()
		f.posts = append(f.posts, string(rawBody))
		f.mu.Unlock()
		if f.respond != nil && f.respond(req, w) {
			return
		}
		w.WriteHeader(f.postCode)
	}))
	f.srv.Listener = l
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *sseFixture) postsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

func TestSSEMultilineDataEvent(t *testing.T) {
	f := newSSEFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr, err := NewSSETransport(ctx, f.srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	f.respond = func(req JSONRPCRequest, w http.ResponseWriter) bool {
		// Pretty-printed JSON split across data: lines.
		f.events <- "event: message\ndata: {\ndata:   \"jsonrpc\": \"2.0\",\ndata:   \"id\": 5,\ndata:   \"result\": {\"ok\": true}\ndata: }\n\n"
		w.WriteHeader(http.StatusAccepted)
		return true
	}
	resp, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: "2.0", ID: int64(5), Method: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.Result), "true") {
		t.Errorf("unexpected result %s", resp.Result)
	}
}

func TestSSEServerRequestGetsAnswerAndDoesNotStealResponse(t *testing.T) {
	f := newSSEFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr, err := NewSSETransport(ctx, f.srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	f.respond = func(req JSONRPCRequest, w http.ResponseWriter) bool {
		if req.Method == "x" {
			// Server-initiated ping reusing the pending request id, then the real response.
			f.events <- "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"ping\"}\n\n"
			go func() {
				time.Sleep(150 * time.Millisecond)
				f.events <- "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{\"real\":true}}\n\n"
			}()
			w.WriteHeader(http.StatusAccepted)
			return true
		}
		return false
	}
	resp, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: "2.0", ID: int64(9), Method: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.Result), "real") {
		t.Fatalf("response stolen by server request: %s / %v", resp.Result, resp.Error)
	}
	answered := false
	for _, p := range f.postsSnapshot() {
		if strings.Contains(p, `"result"`) && strings.Contains(p, `"id":9`) {
			answered = true
		}
	}
	if !answered {
		t.Errorf("client never answered the server's ping; posts=%v", f.postsSnapshot())
	}
}

func TestSSERejectedPostFailsInsteadOfHanging(t *testing.T) {
	f := newSSEFixture(t)
	f.postCode = http.StatusUnauthorized
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr, err := NewSSETransport(ctx, f.srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	start := time.Now()
	_, err = tr.Send(ctx, &JSONRPCRequest{JSONRPC: "2.0", ID: int64(1), Method: "x"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected a 401 error, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("rejected POST waited for the SSE stream")
	}
	if err := tr.Notify(ctx, &JSONRPCRequest{JSONRPC: "2.0", Method: "n"}); err == nil {
		t.Error("Notify swallowed an HTTP 401")
	}
}

func TestSSEStreamEndFailsLaterSends(t *testing.T) {
	f := newSSEFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr, err := NewSSETransport(ctx, f.srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	f.events <- "CLOSE"
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	_, err = tr.Send(ctx, &JSONRPCRequest{JSONRPC: "2.0", ID: int64(2), Method: "x"})
	if err == nil {
		t.Fatal("expected error after the stream ended")
	}
	if time.Since(start) > time.Second {
		t.Errorf("Send waited %s on a dead stream", time.Since(start))
	}
}

func TestSSEConstructionHonorsCallerContext(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no local sockets")
	}
	defer l.Close()
	go func() { // accepts but never answers
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = NewSSETransport(ctx, "http://"+l.Addr().String(), nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("construction ignored the caller deadline (%s)", time.Since(start))
	}
}

// ---- client / adapter ----

type pagedTransport struct {
	pages     map[string]ListToolsResult
	cursors   []string
	notifyErr error
}

func (p *pagedTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	var params struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(req.Params, &params)
	p.cursors = append(p.cursors, params.Cursor)
	b, _ := json.Marshal(p.pages[params.Cursor])
	return &JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: b}, nil
}
func (p *pagedTransport) Notify(ctx context.Context, req *JSONRPCRequest) error { return p.notifyErr }
func (p *pagedTransport) Close() error                                          { return nil }

func TestListToolsFollowsCursors(t *testing.T) {
	c2, c3 := "page2", "page3"
	pt := &pagedTransport{pages: map[string]ListToolsResult{
		"":      {Tools: []Tool{{Name: "a"}}, NextCursor: &c2},
		"page2": {Tools: []Tool{{Name: "b"}}, NextCursor: &c3},
		"page3": {Tools: []Tool{{Name: "c"}}},
	}}
	got, err := NewClient(pt).ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Name != "c" {
		t.Fatalf("incomplete catalog: %+v", got)
	}
}

func TestListToolsRejectsCursorLoop(t *testing.T) {
	loop := "again"
	pt := &pagedTransport{pages: map[string]ListToolsResult{
		"":      {Tools: []Tool{{Name: "a"}}, NextCursor: &loop},
		"again": {Tools: []Tool{{Name: "b"}}, NextCursor: &loop},
	}}
	if _, err := NewClient(pt).ListTools(context.Background()); err == nil {
		t.Fatal("endless cursor loop not detected")
	}
}

func TestInitializeReportsNotificationFailureAndAdvertisesNothing(t *testing.T) {
	init, _ := json.Marshal(InitializeResult{ProtocolVersion: ProtocolVersion})
	var sent []byte
	tr := &captureTransport{result: init, onSend: func(b []byte) { sent = b }, notifyErr: fmt.Errorf("boom")}
	_, err := NewClient(tr).Initialize(context.Background(), "x", "1")
	if err == nil {
		t.Fatal("failed initialized notification was swallowed")
	}
	if strings.Contains(string(sent), "sampling") || strings.Contains(string(sent), "roots") {
		t.Errorf("client advertised unsupported capabilities: %s", sent)
	}
}

type captureTransport struct {
	result    json.RawMessage
	onSend    func([]byte)
	notifyErr error
}

func (c *captureTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	c.onSend(req.Params)
	return &JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: c.result}, nil
}
func (c *captureTransport) Notify(ctx context.Context, req *JSONRPCRequest) error { return c.notifyErr }
func (c *captureTransport) Close() error                                          { return nil }

func TestAdapterNamesDoNotCollide(t *testing.T) {
	names := map[string]string{}
	cases := [][2]string{
		{"srv", "read-file"}, {"srv", "read_file"}, {"a-b", "c"}, {"a_b", "c"}, {"a", "b-c"}, {"a-b", "c"},
		{"srv", "tool with spaces"}, {"srv", "tool_with_spaces"}, {"srv", strings.Repeat("long", 30)}, {"srv", strings.Repeat("long", 31)},
	}
	for i, c := range cases {
		n := NewMCPToolAdapter(c[0], nil, Tool{Name: c[1]}).Name()
		if len(n) > 64 {
			t.Errorf("name too long (%d): %s", len(n), n)
		}
		for _, r := range n {
			if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				t.Errorf("invalid char %q in %s", r, n)
			}
		}
		key := c[0] + "|" + c[1]
		if prev, dup := names[n]; dup && prev != key {
			t.Errorf("collision: %q and %q both map to %s", prev, key, n)
		}
		names[n] = key
		_ = i
	}
}

func TestManagerReusesConnectionAndRegistryRejectsDuplicates(t *testing.T) {
	m := NewManager()
	cl := NewClient(&mockTransport{})
	m.clients["s"] = cl
	m.configs["s"] = ServerConfig{Name: "s", Transport: "stdio", Command: "x"}
	got, err := m.Connect(context.Background(), "s")
	if err != nil || got != cl {
		t.Fatalf("existing connection not reused: %v %v", got, err)
	}
}
