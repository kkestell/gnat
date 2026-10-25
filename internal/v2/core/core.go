// Package core owns scheduling and execution controls independently of transports.
package core

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/kkestell/gnat/internal/v2/store"
)

type Adapter interface {
	Probe(context.Context, store.Settings) error
	Run(context.Context, *store.Claim, func(map[string]any) error) error
}

type execution struct {
	cancel   context.CancelFunc
	done     chan struct{}
	stopping bool
}
type Service struct {
	mu      sync.Mutex
	db      *store.Store
	adapter Adapter
	active  map[string]*execution
	closing bool
	wg      sync.WaitGroup
}

func New(db *store.Store, adapter Adapter) (*Service, error) {
	if err := db.Recover(); err != nil {
		return nil, err
	}
	s := &Service{db: db, adapter: adapter, active: map[string]*execution{}}
	all, err := db.List()
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		if v.Scheduling == "running" && v.Orchestrator.Lifecycle != "error" && v.Orchestrator.Lifecycle != "interrupted" {
			s.Open(v.ID)
		}
	}
	return s, nil
}

// Open checks local prerequisites without running a model turn.
func (s *Service) Open(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.db.Snapshot(id)
	if err == nil && (v.Orchestrator.Lifecycle == "pending" || v.Orchestrator.Lifecycle == "ready") {
		s.prepare(id)
	}
}
func (s *Service) prepare(id string) {
	if s.closing || s.active[id] != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &execution{cancel: cancel, done: make(chan struct{})}
	s.active[id] = e
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer close(e.done)
		v, err := s.db.Snapshot(id)
		if err == nil {
			err = s.adapter.Probe(ctx, v.Orchestrator.Settings)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.active, id)
		if s.closing || ctx.Err() != nil {
			return
		}
		if dbErr := s.db.Ready(id, err); dbErr != nil {
			log.Printf("orchestrator readiness: %v", dbErr)
			return
		}
		if err == nil {
			s.schedule(id)
		}
	}()
}
func (s *Service) Mutate(id, requestID, kind, text, targetTurn string) (store.Result, error) {
	s.mu.Lock()
	// A resume is accepted after cancellation cleanup has persisted its outcome,
	// so that finishing the interrupted turn cannot overwrite the new scheduling state.
	for !s.closing && (kind == "resume" || kind == "retry") {
		e := s.active[id]
		if e == nil || !e.stopping {
			break
		}
		s.mu.Unlock()
		<-e.done
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	if s.closing {
		return store.Result{}, fmt.Errorf("daemon is stopping")
	}
	result, replay, err := s.db.Mutate(id, requestID, kind, text, targetTurn)
	if err != nil || replay {
		return result, err
	}
	switch kind {
	case "stop", "interrupt":
		if e := s.active[id]; e != nil {
			e.stopping = true
			e.cancel()
		}
	case "retry", "resume", "reset":
		if s.active[id] == nil {
			s.prepare(id)
		}
	case "send":
		s.schedule(id)
	}
	return result, nil
}
func (s *Service) schedule(id string) {
	if s.closing || s.active[id] != nil {
		return
	}
	c, err := s.db.Claim(id)
	if err != nil {
		log.Printf("claim input: %v", err)
		return
	}
	if c == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &execution{cancel: cancel, done: make(chan struct{})}
	s.active[id] = e
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer close(e.done)
		err := s.adapter.Run(ctx, c, func(e map[string]any) error { return s.db.Append(c, e) })
		s.mu.Lock()
		defer s.mu.Unlock()
		state, reason := "completed", ""
		if ctx.Err() != nil {
			state, reason = "interrupted", "Execution interrupted; delivered input will not be replayed"
		} else if err != nil {
			state, reason = "error", err.Error()
		}
		if err := s.db.Finish(c, state, reason); err != nil {
			log.Printf("persist turn outcome: %v", err)
			delete(s.active, id)
			return
		}
		delete(s.active, id)
		if state == "completed" {
			s.schedule(id)
		}
	}()
}

// Close prevents new scheduling, interrupts children, and waits for their reaping
// and durable outcomes before the daemon closes SQLite.
func (s *Service) Close() {
	s.mu.Lock()
	s.closing = true
	for _, e := range s.active {
		e.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
