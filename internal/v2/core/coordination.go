package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
)

func (s *Service) Coordinate(b Binding, kind string, c store.Command) (store.Result, error) {
	s.mu.Lock()
	for !s.closing && kind == "send_message" {
		pending := s.active[c.AgentID]
		if pending == nil || !pending.stopping || pending.removing {
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
	caller, err := s.db.Agent(b.SwarmID, b.AgentID)
	if err != nil || caller.Membership != "active" {
		return store.Result{}, fmt.Errorf("caller is unavailable")
	}
	if kind != "send_message" && caller.Role != "orchestrator" {
		return store.Result{}, fmt.Errorf("only the orchestrator may manage workers")
	}
	if r, replay, err := s.db.Replay(b.SwarmID, b.AgentID, kind, c); err != nil || replay {
		if err == nil && kind == "create_agent" {
			if a, e := s.db.Agent(b.SwarmID, r.AgentID); e == nil && a.Membership == "provisioning_failed" {
				if err = s.db.WorkerState(b.SwarmID, a.ID, "provisioning", a.Stage, ""); err == nil {
					a.Membership = "provisioning"
					s.provision(b.SwarmID, a)
				}
			}
		}
		if err == nil && kind == "remove_agent" {
			if a, e := s.db.Agent(b.SwarmID, c.AgentID); e == nil && a.Membership == "removing" {
				s.remove(b.SwarmID, a)
			}
		}
		return r, err
	}
	var base string
	var settings store.Settings
	if kind == "create_agent" {
		v, e := s.db.Snapshot(b.SwarmID)
		if e != nil {
			return store.Result{}, e
		}
		base, err = workspace.ResolveBase(context.Background(), v.Workspace.Root, c.Base)
		if err != nil {
			return store.Result{}, err
		}
		settings = store.ResolveSettings(v.WorkerDefaults, store.Settings{Harness: c.Harness, Model: c.Model, Effort: c.Effort})
		if err := settings.Validate(); err != nil {
			return store.Result{}, err
		}
	}
	r, _, err := s.db.Coordinate(b.SwarmID, b.AgentID, kind, c, base, settings)
	if err != nil {
		return r, err
	}
	switch kind {
	case "create_agent":
		a, _ := s.db.Agent(b.SwarmID, r.AgentID)
		s.provision(b.SwarmID, a)
	case "send_message":
		target, e := s.db.Agent(b.SwarmID, c.AgentID)
		if e == nil && target.Role == "worker" && (target.Lifecycle == "error" || target.Lifecycle == "interrupted") {
			s.prepare(b.SwarmID, target)
		} else {
			s.schedule(b.SwarmID, c.AgentID)
		}
	case "interrupt_agent":
		if e := s.active[c.AgentID]; e != nil {
			e.stopping = true
			e.cancel()
		}
	case "remove_agent":
		a, _ := s.db.Agent(b.SwarmID, c.AgentID)
		s.remove(b.SwarmID, a)
	}
	return r, nil
}
func (s *Service) provision(id string, a store.Agent) {
	if s.closing || s.active[a.ID] != nil {
		return
	}
	ctx, e := s.start(a.ID)
	go func() {
		defer s.wg.Done()
		defer e.cancel()
		defer close(e.done)
		v, err := s.db.Snapshot(id)
		stage := a.Stage
		set := func(next string) error {
			if err := s.db.WorkerState(id, a.ID, "provisioning", next, ""); err != nil {
				return err
			}
			stage = next
			return nil
		}
		if err == nil && stage == "reserved" {
			err = workspace.CheckAllocation(ctx, v.Workspace.Root, a.WorkingDir, *a.Branch)
			if err == nil {
				err = set("allocating_branch")
			}
		}
		if err == nil && stage == "allocating_branch" {
			err = workspace.EnsureBranch(ctx, v.Workspace.Root, *a.Branch, a.BaseCommit)
			if err == nil {
				err = set("branch_created")
			}
		}
		if err == nil && stage == "branch_created" {
			err = set("allocating_worktree")
		}
		if err == nil && stage == "allocating_worktree" {
			err = workspace.EnsureWorktree(ctx, v.Workspace.Root, a.WorkingDir, *a.Branch)
			if err == nil {
				err = set("worktree_created")
			}
		}
		if err == nil {
			err = s.adapter.Probe(ctx, a.Settings)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.active, a.ID)
		if ctx.Err() != nil || s.closing {
			return
		}
		membership, reason := "active", ""
		if err != nil {
			membership, reason = "provisioning_failed", err.Error()
		}
		if err = s.db.WorkerState(id, a.ID, membership, stage, reason); err != nil {
			log.Printf("worker provisioning: %v", err)
			return
		}
		if membership == "active" {
			if err = s.db.ReadyAgent(id, a.ID, nil); err != nil {
				log.Printf("worker readiness: %v", err)
				return
			}
			s.schedule(id, a.ID)
		} else {
			s.schedule(id, v.Orchestrator.ID)
		}
	}()
}
func (s *Service) remove(id string, a store.Agent) {
	if s.closing {
		return
	}
	// Stop the existing turn or provisioning operation before touching its worktree.
	previous := s.active[a.ID]
	if previous != nil && previous.removing {
		return
	}
	if previous != nil {
		previous.stopping = true
		previous.cancel()
	}
	if previous != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			<-previous.done
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.closing {
				if current, err := s.db.Agent(id, a.ID); err == nil && current.Membership == "removing" {
					s.remove(id, current)
				}
			}
		}()
		return
	}
	ctx, e := s.start(a.ID)
	e.removing = true
	go func() {
		defer s.wg.Done()
		defer e.cancel()
		defer close(e.done)
		v, err := s.db.Snapshot(id)
		stage := "removing_worktree"
		if a.Stage == "reserved" {
			stage = "reserved"
		}
		if err == nil {
			err = s.db.WorkerState(id, a.ID, "removing", stage, "")
		}
		if err == nil && a.Stage != "reserved" {
			err = workspace.RemoveWorktree(ctx, v.Workspace.Root, a.WorkingDir, *a.Branch)
		}
		if err == nil {
			if a.Stage != "reserved" {
				stage = "removing_branch"
			}
			err = s.db.WorkerState(id, a.ID, "removing", stage, "")
		}
		if err == nil && a.Stage != "reserved" {
			err = workspace.RemoveBranch(ctx, v.Workspace.Root, *a.Branch)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.active, a.ID)
		if s.closing || ctx.Err() != nil {
			return
		}
		membership, reason := "removed", ""
		if err != nil {
			membership, reason = "removing", err.Error()
		}
		if err = s.db.WorkerState(id, a.ID, membership, stage, reason); err != nil {
			log.Printf("worker removal: %v", err)
		}
		if membership == "removed" {
			delete(s.bindings, s.tokens[a.ID])
			delete(s.tokens, a.ID)
			_ = os.Remove(filepath.Dir(a.WorkingDir))
		}
		s.schedule(id, v.Orchestrator.ID)
	}()
}
func (s *Service) instructions(id string, a store.Agent) string {
	all, _ := s.db.Agents(id, false)
	type identity struct {
		ID         string  `json:"id"`
		Role       string  `json:"role"`
		Name       string  `json:"name"`
		State      string  `json:"execution"`
		Membership string  `json:"membership"`
		Dir        string  `json:"working_dir"`
		Branch     *string `json:"branch"`
		TurnID     string  `json:"turn_id,omitempty"`
	}
	records := []identity{}
	for i, v := range all {
		if i >= 50 {
			break
		}
		records = append(records, identity{v.ID, v.Role, v.Name, v.Lifecycle, v.Membership, v.WorkingDir, v.Branch, v.TurnID})
	}
	team, _ := json.Marshal(records)
	snapshot, _ := s.db.Snapshot(id)
	queued := 0
	for _, m := range snapshot.Messages {
		if m.AgentID == a.ID && m.State == "queued" {
			queued++
		}
	}
	return fmt.Sprintf(`Gnat runtime facts: your immutable agent ID is %s; your role is %s. Your working directory is %s. Team records (up to 50; list_agents returns the current team): %s. You have %d additional queued messages, which will be delivered normally. read_agent can inspect prior conversations.
Gnat coordination tools are on the gnat MCP server. All messages have an authenticated sender and recipient; accepted messages queue durably and each starts a turn when the recipient is available. A message arriving during execution waits for the next turn. A turn ending is an execution outcome. Messages are ordinary conversation; Gnat has no task registry or required reporting protocol.
list_agents shows current execution and checkout metadata. read_agent inspects bounded history, including removed agents. send_message sends text to an agent ID. Mutation tools require a unique request_id for each deliberate operation; reuse it only to retry that same operation. Orchestrators also have create_agent, interrupt_agent (with the observed turn_id), and remove_agent. Workers cannot create or remove Gnat agents.
The orchestrator works in the original checkout. Workers have separate managed worktrees starting from committed content, with normal repository instructions and local tools. Dependencies, ignored files, development services, and integration are handled with ordinary tools. Git commits are shared locally; native harness delegation remains available unchanged. Removal uses Git safeguards and can be refused for dirty or unmerged work.`, a.ID, a.Role, a.WorkingDir, string(team), queued)
}
