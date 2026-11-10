package server

import (
	"context"
	"fmt"
	"sync/atomic"
)

// goalSignalsEnabled is test-only. Production does not pay for the per-task
// channel bookkeeping; server tests enable it in TestMain to wait on the real
// bootstrap-goal completion instead of a fixed sleep.
var goalSignalsEnabled atomic.Bool

func enableGoalSignalsForTests() { goalSignalsEnabled.Store(true) }

// ensureGoalsSignal returns (creating if needed) the per-task bootstrap goal
// signal. The channel is closed by markGoalsReady after createGoals returns.
func (s *Server) ensureGoalsSignal(expID int64) chan struct{} {
	if !goalSignalsEnabled.Load() {
		return nil
	}
	s.goalsMu.Lock()
	defer s.goalsMu.Unlock()
	if s.goalsReady == nil {
		s.goalsReady = map[int64]chan struct{}{}
	}
	if ch := s.goalsReady[expID]; ch != nil {
		return ch
	}
	ch := make(chan struct{})
	s.goalsReady[expID] = ch
	return ch
}

func (s *Server) markGoalsReady(expID int64) {
	if !goalSignalsEnabled.Load() {
		return
	}
	s.goalsMu.Lock()
	if s.goalsReady == nil {
		s.goalsReady = map[int64]chan struct{}{}
	}
	ch := s.goalsReady[expID]
	if ch == nil {
		ch = make(chan struct{})
		s.goalsReady[expID] = ch
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
	s.goalsMu.Unlock()
}

// waitGoalsReady blocks until the bootstrap goal round has finished for expID.
// It is used by tests to replace fixed sleeps; it does not change runtime flow.
func (s *Server) waitGoalsReady(ctx context.Context, expID int64) error {
	ch := s.ensureGoalsSignal(expID)
	if ch == nil {
		return fmt.Errorf("goal readiness signals are disabled (test-only)")
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
