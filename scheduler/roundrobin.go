package scheduler

import (
	"sync"
)

// RoundRobinState tracks the last used subflow.
type RoundRobinState struct {
	LastSentSubflow int
}

// RoundRobin implements SchedulerOps.
type RoundRobin struct {
	mu      sync.Mutex // Ensures thread-safety during concurrent writes
	storage map[[16]byte]*RoundRobinState
}

func NewRoundRobin() *RoundRobin {
	return &RoundRobin{
		storage: make(map[[16]byte]*RoundRobinState),
	}
}

func (rr *RoundRobin) Name() string {
	return "roundrobin"
}

func (rr *RoundRobin) Init(conn ConnectionInfo) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.storage[conn.GetConnectionID()] = &RoundRobinState{
		LastSentSubflow: -1,
	}
}

func (rr *RoundRobin) Release(conn ConnectionInfo) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	delete(rr.storage, conn.GetConnectionID())
}

// SelectSubflow directly implements the algorithm from Section 7.3
func (rr *RoundRobin) SelectSubflow(conn ConnectionInfo) int {
	rr.mu.Lock()
	defer rr.mu.Unlock()

	var next int
	state := rr.storage[conn.GetConnectionID()]
	subflowCount := conn.GetSubflowCount()

	// Step 2 & 3: If history exists, find the next sequential subflow
	if state.LastSentSubflow != -1 {
		nextIdx := state.LastSentSubflow + 1
		if nextIdx >= subflowCount {
			next = 0
		} else {
			next = nextIdx
		}
	}

	// Step 6 & 7: Mark and remember choice
	conn.MarkSubflowScheduled(next)
	state.LastSentSubflow = next

	return next
}
