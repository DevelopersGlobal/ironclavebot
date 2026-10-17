package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
)

const wsMagicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WSConn provides a lightweight, pure Go standard-library RFC 6455 WebSocket connection
type WSConn struct {
	conn    net.Conn
	bufrw   *bufio.ReadWriter
	writeMu sync.Mutex
}

// UpgradeHTTP upgrades an HTTP connection to a WebSocket connection
func UpgradeHTTP(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("missing or invalid Upgrade header")
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, errors.New("missing or invalid Connection header")
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key header")
	}

	h := sha1.New()
	h.Write([]byte(key + wsMagicGUID))
	acceptKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("webserver does not support hijacking")
	}

	conn, bufrw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack failed: %w", err)
	}

	res := fmt.Sprintf("HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", acceptKey)

	if _, err := bufrw.WriteString(res); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := bufrw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &WSConn{
		conn:  conn,
		bufrw: bufrw,
	}, nil
}

// ReadMessage reads an unfragmented WebSocket frame
func (ws *WSConn) ReadMessage() (int, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(ws.bufrw, header); err != nil {
		return 0, nil, err
	}

	opcode := int(header[0] & 0x0F)
	masked := (header[1] & 0x80) != 0
	payloadLen := int(header[1] & 0x7F)

	if payloadLen == 126 {
		lenBytes := make([]byte, 2)
		if _, err := io.ReadFull(ws.bufrw, lenBytes); err != nil {
			return 0, nil, err
		}
		payloadLen = int(binary.BigEndian.Uint16(lenBytes))
	} else if payloadLen == 127 {
		lenBytes := make([]byte, 8)
		if _, err := io.ReadFull(ws.bufrw, lenBytes); err != nil {
			return 0, nil, err
		}
		payloadLen = int(binary.BigEndian.Uint64(lenBytes))
	}

	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(ws.bufrw, maskKey); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(ws.bufrw, payload); err != nil {
		return 0, nil, err
	}

	if masked {
		for i := 0; i < payloadLen; i++ {
			payload[i] ^= maskKey[i%4]
		}
	}

	// Handle Ping (opcode 0x9) -> respond with Pong (opcode 0xA)
	if opcode == 0x9 {
		_ = ws.WriteFrame(0xA, payload)
		return ws.ReadMessage()
	}

	// Handle Close (opcode 0x8)
	if opcode == 0x8 {
		_ = ws.WriteFrame(0x8, payload)
		_ = ws.conn.Close()
		return opcode, nil, io.EOF
	}

	return opcode, payload, nil
}

// WriteFrame sends a frame to client (unmasked)
func (ws *WSConn) WriteFrame(opcode int, payload []byte) error {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()

	var header []byte
	header = append(header, byte(0x80|(opcode&0x0F))) // FIN = 1

	payloadLen := len(payload)
	if payloadLen < 126 {
		header = append(header, byte(payloadLen))
	} else if payloadLen <= 65535 {
		header = append(header, 126)
		lenBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(lenBytes, uint16(payloadLen))
		header = append(header, lenBytes...)
	} else {
		header = append(header, 127)
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, uint64(payloadLen))
		header = append(header, lenBytes...)
	}

	if _, err := ws.bufrw.Write(header); err != nil {
		return err
	}
	if _, err := ws.bufrw.Write(payload); err != nil {
		return err
	}
	return ws.bufrw.Flush()
}

// WriteText sends a text frame
func (ws *WSConn) WriteText(text string) error {
	return ws.WriteFrame(0x1, []byte(text))
}

// Close closes the underlying connection
func (ws *WSConn) Close() error {
	return ws.conn.Close()
}

// WSApprovalHub broadcasts interactive approval requests and handles decisions over WebSocket
type WSApprovalHub struct {
	engine  *sandbox.ApprovalEngine
	clients map[*WSConn]bool
	mu      sync.RWMutex
}

// NewWSApprovalHub initializes the hub
func NewWSApprovalHub(engine *sandbox.ApprovalEngine) *WSApprovalHub {
	hub := &WSApprovalHub{
		engine:  engine,
		clients: make(map[*WSConn]bool),
	}
	go hub.listenEngineEvents()
	return hub
}

func (h *WSApprovalHub) listenEngineEvents() {
	if h.engine == nil {
		return
	}
	events, _ := h.engine.Subscribe()
	for req := range events {
		h.broadcastApproval(req)
	}
}

func (h *WSApprovalHub) broadcastApproval(req *sandbox.ApprovalRequest) {
	data, err := json.Marshal(map[string]interface{}{
		"type":    "approval_request",
		"payload": req,
	})
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients {
		_ = client.WriteText(string(data))
	}
}

// HandleWS handles an incoming WebSocket upgrade and interactive loop
func (h *WSApprovalHub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := UpgradeHTTP(w, r)
	if err != nil {
		http.Error(w, "Failed to upgrade websocket: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()
		_ = conn.Close()
	}()

	// Send currently pending approval requests upon initial connection
	if h.engine != nil {
		pending := h.engine.ListPending()
		for _, req := range pending {
			data, _ := json.Marshal(map[string]interface{}{
				"type":    "approval_request",
				"payload": req,
			})
			_ = conn.WriteText(string(data))
		}
	}

	// Read loop for client decisions: {"type": "decision", "id": "...", "approved": true/false}
	for {
		opcode, payload, err := conn.ReadMessage()
		if err != nil || opcode == 0x8 {
			break
		}

		var msg struct {
			Type     string `json:"type"`
			ID       string `json:"id"`
			Approved bool   `json:"approved"`
		}
		if err := json.Unmarshal(payload, &msg); err == nil && msg.Type == "decision" {
			if h.engine != nil && msg.ID != "" {
				if err := h.engine.SubmitDecision(msg.ID, msg.Approved); err != nil {
					log.Printf("⚠️ [WS] Failed to submit decision for %s: %v", msg.ID, err)
				} else {
					log.Printf("🛡️ [Approval] User submitted decision via WebSocket: id=%s approved=%t", msg.ID, msg.Approved)
				}
			}
		}
	}
}
