// Package core owns scheduling and execution controls independently of transports.
package core

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/kkestell/gnat/internal/store"
)

type Adapter interface {
	Probe(context.Context, store.Settings) error
	Run(context.Context, *store.Claim, func(map[string]any) error) error
}
type execution struct {
	cancel   context.CancelFunc
	done     chan struct{}
	stopping bool
	removing bool
}
type Binding struct {
	SwarmID string
	AgentID string
}
type Service struct {
	mu       sync.Mutex
	db       *store.Store
	adapter  Adapter
	active   map[string]*execution
	bindings map[string]Binding
	tokens   map[string]string
	endpoint string
	closing  bool
	wg       sync.WaitGroup
}

func New(db *store.Store, adapter Adapter, endpoint ...string) (*Service, error) {
	if err := db.Recover(); err != nil {
		return nil, err
	}
	s := &Service{db: db, adapter: adapter, active: map[string]*execution{}, bindings: map[string]Binding{}, tokens: map[string]string{}}
	if len(endpoint) > 0 {
		s.endpoint = endpoint[0]
	}
	all, err := db.List()
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		s.Open(v.ID)
	}
	return s, nil
}
func (s *Service) Authenticate(token string) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[token]
	if !ok {
		return b, false
	}
	a, err := s.db.Agent(b.SwarmID, b.AgentID)
	return b, err == nil && a.Membership == "active"
}
func (s *Service) binding(swarm, aid string) string {
	if token := s.tokens[aid]; token != "" {
		return token
	}
	token := store.NewID()
	s.tokens[aid] = token
	s.bindings[token] = Binding{swarm, aid}
	return token
}
func (s *Service) Open(id string) { s.mu.Lock(); defer s.mu.Unlock(); s.open(id, false) }
func (s *Service) open(id string, retry bool) {
	all, err := s.db.Agents(id, false)
	if err != nil {
		return
	}
	v, err := s.db.Snapshot(id)
	if err != nil {
		return
	}
	for _, a := range all {
		switch a.Membership {
		case "provisioning":
			s.provision(id, a)
		case "removing":
			s.remove(id, a)
		case "active":
			if v.Scheduling == "running" && (retry || a.Lifecycle == "ready" || a.Lifecycle == "pending") {
				s.prepare(id, a)
			}
		}
	}
}
func (s *Service) prepare(id string, a store.Agent) {
	if s.closing || s.active[a.ID] != nil {
		return
	}
	ctx, e := s.start(a.ID)
	go func() {
		defer s.wg.Done()
		defer e.cancel()
		defer close(e.done)
		probeErr := s.adapter.Probe(ctx, a.Settings)
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.active, a.ID)
		if s.closing || ctx.Err() != nil {
			return
		}
		if err := s.db.ReadyAgent(id, a.ID, probeErr); err != nil {
			log.Printf("agent readiness: %v", err)
			return
		}
		s.schedule(id, a.ID)
		if probeErr != nil && a.Role == "worker" {
			v, e := s.db.Snapshot(id)
			if e == nil {
				s.schedule(id, v.Orchestrator.ID)
			}
		}
	}()
}
func (s *Service) start(aid string) (context.Context, *execution) {
	ctx, cancel := context.WithCancel(context.Background())
	e := &execution{cancel: cancel, done: make(chan struct{})}
	s.active[aid] = e
	s.wg.Add(1)
	return ctx, e
}
func (s *Service) Mutate(id, requestID, kind, text, targetTurn string) (store.Result, error) {
	s.mu.Lock()
	for !s.closing && (kind == "resume" || kind == "retry") {
		all, err := s.db.Agents(id, false)
		if err != nil {
			break
		}
		var pending *execution
		for _, a := range all {
			if e := s.active[a.ID]; e != nil && e.stopping {
				pending = e
				break
			}
		}
		if pending == nil {
			break
		}
		s.mu.Unlock()
		<-pending.done
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
	v, err := s.db.Snapshot(id)
	if err != nil {
		return result, err
	}
	switch kind {
	case "stop":
		all, _ := s.db.Agents(id, false)
		for _, a := range all {
			if e := s.active[a.ID]; e != nil {
				e.stopping = true
				e.cancel()
			}
		}
	case "interrupt":
		if e := s.active[v.Orchestrator.ID]; e != nil {
			e.stopping = true
			e.cancel()
		}
	case "retry", "resume":
		s.open(id, true)
	case "reset":
		s.prepare(id, v.Orchestrator)
	case "send":
		s.schedule(id, v.Orchestrator.ID)
	}
	return result, nil
}
func (s *Service) schedule(id, aid string) {
	if s.closing || s.active[aid] != nil {
		return
	}
	c, err := s.db.ClaimAgent(id, aid)
	if err != nil {
		log.Printf("claim input: %v", err)
		return
	}
	if c == nil {
		return
	}
	c.MCPURL = s.endpoint
	c.MCPToken = s.binding(id, aid)
	c.Instructions = s.instructions(id, c.Agent)
	ctx, e := s.start(aid)
	go func() {
		defer s.wg.Done()
		defer e.cancel()
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
			delete(s.active, aid)
			return
		}
		delete(s.active, aid)
		if state == "completed" {
			s.schedule(id, aid)
		}
		if c.Agent.Role == "worker" && state != "completed" {
			v, err := s.db.Snapshot(id)
			if err == nil {
				s.schedule(id, v.Orchestrator.ID)
			}
		}
	}()
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closing = true
	for _, e := range s.active {
		e.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
