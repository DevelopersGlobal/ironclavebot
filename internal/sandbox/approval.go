package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RiskLevel defines the potential danger of an operation
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// ApprovalStatus represents the current state of an approval request
type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusRejected ApprovalStatus = "rejected"
	ApprovalStatusTimeout  ApprovalStatus = "timeout"
)

// ApprovalRequest represents an interactive permission request
type ApprovalRequest struct {
	ID          string         `json:"id"`
	SessionID   string         `json:"session_id"`
	ToolName    string         `json:"tool_name"`
	Arguments   string         `json:"arguments"`
	Explanation string         `json:"explanation"`
	RiskLevel   RiskLevel      `json:"risk_level"`
	Status      ApprovalStatus `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	DecidedAt   time.Time      `json:"decided_at,omitempty"`

	decideChan chan bool `json:"-"`
}

// ApprovalEngine enforces granular permission boundaries across tools
type ApprovalEngine struct {
	mu             sync.RWMutex
	policy         Policy
	defaultTimeout time.Duration
	pending        map[string]*ApprovalRequest
	subscribers    map[uint64]chan *ApprovalRequest
	subCounter     atomic.Uint64
	reqCounter     atomic.Uint64
}

// NewApprovalEngine initializes an approval engine with a given policy tier
func NewApprovalEngine(policy Policy, defaultTimeout time.Duration) *ApprovalEngine {
	if defaultTimeout <= 0 {
		defaultTimeout = 60 * time.Second
	}
	return &ApprovalEngine{
		policy:         policy,
		defaultTimeout: defaultTimeout,
		pending:        make(map[string]*ApprovalRequest),
		subscribers:    make(map[uint64]chan *ApprovalRequest),
	}
}

// Policy returns current policy tier
func (e *ApprovalEngine) Policy() Policy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.policy
}

// SetPolicy updates the active policy tier
func (e *ApprovalEngine) SetPolicy(p Policy) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policy = p
}

// AssessRisk determines the risk tier of a tool call
func (e *ApprovalEngine) AssessRisk(toolName string, argsJSON string) RiskLevel {
	lower := strings.ToLower(toolName)
	if strings.Contains(lower, "command") || strings.Contains(lower, "terminal") || strings.Contains(lower, "exec") {
		return RiskHigh
	}
	if strings.Contains(lower, "write") || strings.Contains(lower, "delete") || strings.Contains(lower, "patch") {
		return RiskMedium
	}
	if strings.Contains(lower, "read") || strings.Contains(lower, "list") || strings.Contains(lower, "get") {
		return RiskLow
	}
	// External MCP tools default to Medium risk unless recognized
	return RiskMedium
}

// RequiresApproval determines whether a tool execution requires human authorization
func (e *ApprovalEngine) RequiresApproval(toolName string, argsJSON string) bool {
	e.mu.RLock()
	currentPolicy := e.policy
	e.mu.RUnlock()

	switch currentPolicy {
	case PolicyStrict:
		// Strict requires approval for all operations (including read)
		return true

	case PolicyPermissive:
		// Permissive allows execution within sandbox bounds without interactive prompt
		return false

	case PolicyInteractive:
		fallthrough
	default:
		// Interactive auto-approves low-risk (read) and prompts for medium/high risk
		risk := e.AssessRisk(toolName, argsJSON)
		return risk != RiskLow
	}
}

// RequestApproval creates a pending approval and waits for user decision or timeout
func (e *ApprovalEngine) RequestApproval(ctx context.Context, sessionID, toolName, argsJSON, explanation string) (bool, error) {
	risk := e.AssessRisk(toolName, argsJSON)
	reqID := fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), e.reqCounter.Add(1))

	req := &ApprovalRequest{
		ID:          reqID,
		SessionID:   sessionID,
		ToolName:    toolName,
		Arguments:   argsJSON,
		Explanation: explanation,
		RiskLevel:   risk,
		Status:      ApprovalStatusPending,
		CreatedAt:   time.Now(),
		decideChan:  make(chan bool, 1),
	}

	e.mu.Lock()
	e.pending[reqID] = req
	e.mu.Unlock()

	e.broadcast(req)

	timeout := e.defaultTimeout
	select {
	case approved := <-req.decideChan:
		return approved, nil

	case <-time.After(timeout):
		e.mu.Lock()
		req.Status = ApprovalStatusTimeout
		req.DecidedAt = time.Now()
		delete(e.pending, reqID)
		e.mu.Unlock()
		e.broadcast(req)
		return false, fmt.Errorf("permission approval request timed out after %v", timeout)

	case <-ctx.Done():
		e.mu.Lock()
		req.Status = ApprovalStatusTimeout
		req.DecidedAt = time.Now()
		delete(e.pending, reqID)
		e.mu.Unlock()
		e.broadcast(req)
		return false, ctx.Err()
	}
}

// SubmitDecision resolves a pending approval request
func (e *ApprovalEngine) SubmitDecision(id string, approved bool) error {
	e.mu.Lock()
	req, exists := e.pending[id]
	if !exists {
		e.mu.Unlock()
		return fmt.Errorf("approval request %q not found or already resolved", id)
	}

	if approved {
		req.Status = ApprovalStatusApproved
	} else {
		req.Status = ApprovalStatusRejected
	}
	req.DecidedAt = time.Now()
	delete(e.pending, id)
	e.mu.Unlock()

	select {
	case req.decideChan <- approved:
	default:
	}

	e.broadcast(req)
	return nil
}

// ListPending returns all currently pending approval requests
func (e *ApprovalEngine) ListPending() []*ApprovalRequest {
	e.mu.RLock()
	defer e.mu.RUnlock()

	list := make([]*ApprovalRequest, 0, len(e.pending))
	for _, req := range e.pending {
		list = append(list, req)
	}
	return list
}

// GetRequest retrieves an approval request by ID
func (e *ApprovalEngine) GetRequest(id string) (*ApprovalRequest, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	req, exists := e.pending[id]
	return req, exists
}

// Subscribe returns a channel receiving approval events and an unsubscribe cleanup func
func (e *ApprovalEngine) Subscribe() (chan *ApprovalRequest, func()) {
	subID := e.subCounter.Add(1)
	ch := make(chan *ApprovalRequest, 16)

	e.mu.Lock()
	e.subscribers[subID] = ch
	e.mu.Unlock()

	unsub := func() {
		e.mu.Lock()
		delete(e.subscribers, subID)
		close(ch)
		e.mu.Unlock()
	}

	return ch, unsub
}

func (e *ApprovalEngine) broadcast(req *ApprovalRequest) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, ch := range e.subscribers {
		select {
		case ch <- req:
		default:
		}
	}
}
