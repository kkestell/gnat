package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/store"
	"github.com/kkestell/gnat/internal/workspace"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Fixture")
	git(t, root, "config", "user.email", "fixture@example.test")
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "file")
	git(t, root, "commit", "-m", "initial")
	w, err := workspace.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	return w.Root
}
func workerState(t *testing.T, db *store.Store, swarm, aid string, predicate func(store.Agent) bool) store.Agent {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		a, err := db.Agent(swarm, aid)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(a) {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	a, _ := db.Agent(swarm, aid)
	t.Fatalf("worker state timed out: %+v", a)
	return a
}
func coordinate(t *testing.T, s *Service, b Binding, kind string, c store.Command) store.Result {
	t.Helper()
	if c.RequestID == "" {
		c.RequestID = store.NewID()
	}
	r, err := s.Coordinate(b, kind, c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestDynamicWorkersMessagingAndSafeRemoval(t *testing.T) {
	root := repository(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w, _ := workspace.Discover(root)
	v, err := db.OpenWorkspace(w, func() (store.Settings, error) {
		return store.Settings{Harness: "claude", Model: "fixture", Effort: "high"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{runs: make(chan *run, 20)}
	s, err := New(db, f, "http://127.0.0.1:1234/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ready(t, db, v.ID, "ready")
	b := Binding{v.ID, v.Orchestrator.ID}
	// Worker allocation never copies or changes the dirty/staged original checkout.
	if err = os.WriteFile(filepath.Join(root, "file"), []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "file")
	before := git(t, root, "status", "--porcelain")
	create := store.Command{RequestID: "create-one", Name: "one", Prompt: "initial one"}
	a := coordinate(t, s, b, "create_agent", create)
	replay := coordinate(t, s, b, "create_agent", create)
	if replay != a {
		t.Fatal("creation replay changed identity")
	}
	two := coordinate(t, s, b, "create_agent", store.Command{Name: "two", Prompt: "initial two"})
	first, second := next(t, f), next(t, f)
	runs := map[string]*run{first.claim.Agent.ID: first, second.claim.Agent.ID: second}
	ra, rb := runs[a.AgentID], runs[two.AgentID]
	if ra == nil || rb == nil {
		t.Fatal("missing workers")
	}
	if before != git(t, root, "status", "--porcelain") {
		t.Fatal("allocation changed original")
	}
	for _, r := range runs {
		c := r.claim
		if c.Agent.WorkingDir == root || c.Agent.Branch == nil || c.Agent.ConversationID == v.Orchestrator.ConversationID || c.MCPURL == "" || c.MCPToken == "" {
			t.Fatal("lost worker isolation", c)
		}
		data, err := os.ReadFile(filepath.Join(c.Agent.WorkingDir, "file"))
		if err != nil || string(data) != "committed" {
			t.Fatal("worker copied local edits", err)
		}
		if b, ok := s.Authenticate(c.MCPToken); !ok || b.AgentID != c.Agent.ID {
			t.Fatal("incorrect credential binding")
		}
	}
	wb := Binding{v.ID, a.AgentID}
	if _, err = s.Coordinate(wb, "create_agent", store.Command{RequestID: "bad", Name: "forbidden", Prompt: "bad"}); err == nil {
		t.Fatal("worker created a worker")
	}
	other := newWorkspace(t, db, "/other")
	if _, err = s.Coordinate(wb, "send_message", store.Command{RequestID: "cross", AgentID: other.Orchestrator.ID, Text: "bad"}); err == nil {
		t.Fatal("cross-swarm message accepted")
	}
	direction := store.Command{RequestID: "direction", AgentID: a.AgentID, Text: "follow-up"}
	sent := coordinate(t, s, b, "send_message", direction)
	if got := coordinate(t, s, b, "send_message", direction); got != sent {
		t.Fatal("duplicate message")
	}
	coordinate(t, s, wb, "send_message", store.Command{AgentID: v.Orchestrator.ID, Text: "ordinary result"})
	orchestrator := next(t, f)
	if orchestrator.claim.Agent.ID != v.Orchestrator.ID || orchestrator.claim.Text != "ordinary result" || orchestrator.claim.Sender != a.AgentID {
		t.Fatal("message failed to start orchestrator turn", orchestrator.claim)
	}
	orchestrator.done <- nil
	ready(t, db, v.ID, "ready")
	ra.done <- nil
	follow := next(t, f)
	if follow.claim.Agent.ID != a.AgentID || follow.claim.Text != "follow-up" || follow.claim.SessionRef == "" {
		t.Fatal("follow-up lost continuity", follow.claim)
	}
	// Interrupt exactly one worker while the other stays running.
	coordinate(t, s, b, "interrupt_agent", store.Command{AgentID: a.AgentID, TurnID: follow.claim.Turn.ID})
	workerState(t, db, v.ID, a.AgentID, func(a store.Agent) bool { return a.Lifecycle == "interrupted" })
	select {
	case <-rb.ended:
		t.Fatal("interrupted another worker")
	default:
	}
	notice := next(t, f)
	if notice.claim.Sender != "system" {
		t.Fatal("failure notice missing")
	}
	notice.done <- nil
	ready(t, db, v.ID, "ready")
	coordinate(t, s, b, "send_message", store.Command{AgentID: a.AgentID, Text: "continue after interrupt"})
	continuation := next(t, f)
	if continuation.claim.Agent.ID != a.AgentID || continuation.claim.Text != "continue after interrupt" {
		t.Fatal("ordinary direction did not continue worker", continuation.claim)
	}
	continuation.done <- nil
	workerState(t, db, v.ID, a.AgentID, func(a store.Agent) bool { return a.Lifecycle == "ready" })
	// Dirty work must remain visible after a removal refusal; queued input is cancelled.
	if err = os.WriteFile(filepath.Join(rb.claim.Agent.WorkingDir, "untracked"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	coordinate(t, s, b, "send_message", store.Command{AgentID: two.AgentID, Text: "queued"})
	remove := store.Command{RequestID: "remove-two", AgentID: two.AgentID}
	coordinate(t, s, b, "remove_agent", remove)
	workerState(t, db, v.ID, two.AgentID, func(a store.Agent) bool { return a.Membership == "removing" && a.Error != "" })
	if _, err = os.Stat(filepath.Join(rb.claim.Agent.WorkingDir, "untracked")); err != nil {
		t.Fatal("dirty work deleted", err)
	}
	if _, err = s.Coordinate(wb, "send_message", store.Command{RequestID: "late", AgentID: two.AgentID, Text: "late"}); err == nil {
		t.Fatal("message accepted during removal")
	}
	if err = os.Remove(filepath.Join(rb.claim.Agent.WorkingDir, "untracked")); err != nil {
		t.Fatal(err)
	}
	coordinate(t, s, b, "remove_agent", remove)
	workerState(t, db, v.ID, two.AgentID, func(a store.Agent) bool { return a.Membership == "removed" })
	if _, ok := s.Authenticate(rb.claim.MCPToken); ok {
		t.Fatal("removed credential remained active")
	}
	got, err := db.Snapshot(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workers) != 1 || len(got.RemovedWorkers) != 1 {
		t.Fatal("membership did not update", got.Workers)
	}
	cancelled := false
	for _, m := range got.Messages {
		if m.Text == "queued" && m.State == "cancelled" {
			cancelled = true
		}
	}
	if !cancelled {
		t.Fatal("removed queue not cancelled")
	}
	history, err := db.ReadAgent(v.ID, two.AgentID, 0, 2)
	if err != nil || len(history["events"].([]map[string]any)) != 2 {
		t.Fatal("removed bounded history unavailable", err)
	}
}
func TestProvisioningRecoveryAndUnmergedBranchRefusal(t *testing.T) {
	root := repository(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, root)
	base := git(t, root, "rev-parse", "HEAD")
	c := store.Command{RequestID: "provision", Name: "recover", Prompt: "start"}
	r, _, err := db.Coordinate(v.ID, v.Orchestrator.ID, "create_agent", c, base, v.Orchestrator.Settings)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := db.Agent(v.ID, r.AgentID)
	if err = db.WorkerState(v.ID, a.ID, "provisioning", "allocating_branch", ""); err != nil {
		t.Fatal(err)
	}
	git(t, root, "branch", *a.Branch, base)
	// New resumes a durable allocation after branch creation, preserving identity.
	f := &fake{runs: make(chan *run, 20)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := next(t, f)
	if run.claim.Agent.ID != a.ID {
		t.Fatal("recovery duplicated worker")
	}
	run.done <- nil
	workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Lifecycle == "ready" })
	if err = os.WriteFile(filepath.Join(a.WorkingDir, "new"), []byte("committed work"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, a.WorkingDir, "add", "new")
	git(t, a.WorkingDir, "commit", "-m", "worker change")
	b := Binding{v.ID, v.Orchestrator.ID}
	remove := store.Command{RequestID: "remove", AgentID: a.ID}
	coordinate(t, s, b, "remove_agent", remove)
	failed := workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Membership == "removing" && a.Error != "" })
	if failed.Stage != "removing_branch" {
		t.Fatal("incorrect failed stage", failed)
	}
	if got := git(t, root, "show", *a.Branch+":new"); got != "committed work" {
		t.Fatal("unmerged commit lost")
	}
	git(t, root, "merge", "--ff-only", *a.Branch)
	coordinate(t, s, b, "remove_agent", remove)
	workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Membership == "removed" })
}
func TestAllocationCollisionNeverDeletesExistingResources(t *testing.T) {
	root := repository(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, root)
	base := git(t, root, "rev-parse", "HEAD")
	r, _, err := db.Coordinate(v.ID, v.Orchestrator.ID, "create_agent", store.Command{RequestID: "create", Name: "collision", Prompt: "start"}, base, v.Orchestrator.Settings)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := db.Agent(v.ID, r.AgentID)
	git(t, root, "branch", *a.Branch, base)
	f := &fake{runs: make(chan *run, 20)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Membership == "provisioning_failed" })
	coordinate(t, s, Binding{v.ID, v.Orchestrator.ID}, "remove_agent", store.Command{AgentID: a.ID})
	workerState(t, db, v.ID, a.ID, func(a store.Agent) bool { return a.Membership == "removed" })
	if exists, err := workspace.BranchExists(context.Background(), root, *a.Branch); err != nil || !exists {
		t.Fatal("collision branch deleted", err)
	}
}
func TestSwarmStopAndRestartPreservesWorkers(t *testing.T) {
	root := repository(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, root)
	f := &fake{runs: make(chan *run, 20)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b := Binding{v.ID, v.Orchestrator.ID}
	coordinate(t, s, b, "create_agent", store.Command{Name: "a", Prompt: "a"})
	coordinate(t, s, b, "create_agent", store.Command{Name: "b", Prompt: "b"})
	a, bb := next(t, f), next(t, f)
	coordinate(t, s, b, "send_message", store.Command{AgentID: a.claim.Agent.ID, Text: "later"})
	call(t, s, v.ID, "stop", "")
	<-a.ended
	<-bb.ended
	call(t, s, v.ID, "resume", "")
	later := next(t, f)
	for later.claim.Agent.ID == v.Orchestrator.ID {
		later.done <- nil
		later = next(t, f)
	}
	if later.claim.Text != "later" {
		t.Fatal("interrupted input replayed", later.claim.Text)
	}
	later.done <- fmt.Errorf("fixture failure")
	notice := next(t, f)
	if notice.claim.Agent.ID != v.Orchestrator.ID {
		t.Fatal("worker failure did not wake orchestrator")
	}
	notice.done <- nil
	s.Close()
	s, err = New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, err := db.Agents(v.ID, false)
	if err != nil || len(all) != 3 {
		t.Fatal("lost team on restart", all, err)
	}
}

func TestWorkerDirectionWaitsForInterruptionToSettle(t *testing.T) {
	root := repository(t)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, root)
	cleanup := make(chan struct{})
	released := false
	f := &fake{runs: make(chan *run, 20), cleanup: cleanup, cancelled: make(chan struct{}, 20)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !released {
			close(cleanup)
		}
		s.Close()
	}()
	b := Binding{v.ID, v.Orchestrator.ID}
	worker := coordinate(t, s, b, "create_agent", store.Command{Name: "worker", Prompt: "initial"})
	first := next(t, f)
	coordinate(t, s, b, "interrupt_agent", store.Command{AgentID: worker.AgentID, TurnID: first.claim.Turn.ID})
	<-f.cancelled
	direction := store.Command{RequestID: "continue", AgentID: worker.AgentID, Text: "continue immediately"}
	continued := make(chan error, 1)
	go func() {
		_, err := s.Coordinate(b, "send_message", direction)
		continued <- err
	}()
	select {
	case err := <-continued:
		t.Fatal("direction acknowledged before interruption settled", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(cleanup)
	released = true
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("direction never acknowledged")
	}
	second := next(t, f)
	for second.claim.Agent.ID == v.Orchestrator.ID {
		second.done <- nil
		second = next(t, f)
	}
	if second.claim.Agent.ID != worker.AgentID || second.claim.Text != direction.Text || second.claim.SessionRef != "session-"+worker.AgentID {
		t.Fatal("lost worker continuation", second.claim)
	}
	coordinate(t, s, b, "send_message", direction)
	second.done <- nil
	workerState(t, db, v.ID, worker.AgentID, func(a store.Agent) bool { return a.Lifecycle == "ready" })
	got, err := db.Snapshot(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range got.Messages {
		if m.AgentID == worker.AgentID && m.Text == direction.Text {
			count++
		}
	}
	if count != 1 {
		t.Fatal("retried direction duplicated input", count)
	}
}

func TestConversationResetPreservesTeamAndQueuedInput(t *testing.T) {
	root := repository(t)
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, root)
	f := &fake{runs: make(chan *run, 20)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ready(t, db, v.ID, "ready")
	b := Binding{v.ID, v.Orchestrator.ID}
	worker := coordinate(t, s, b, "create_agent", store.Command{Name: "kept", Prompt: "worker work"})
	wr := next(t, f)
	workerSession := wr.claim.Agent.ConversationID
	// The worker remains active across the orchestrator's settled reset.
	call(t, s, v.ID, "send", "original conversation")
	original := next(t, f)
	original.done <- nil
	ready(t, db, v.ID, "ready")
	call(t, s, v.ID, "pause", "")
	queued := call(t, s, v.ID, "send", "queued before reset")
	before, err := db.Snapshot(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	reset := call(t, s, v.ID, "reset", "")
	after := ready(t, db, v.ID, "ready")
	if after.ID != before.ID || after.Orchestrator.ID != before.Orchestrator.ID || after.Orchestrator.WorkingDir != root || len(after.Workers) != 1 || after.Workers[0].ID != worker.AgentID || after.Workers[0].WorkingDir != before.Workers[0].WorkingDir || after.Workers[0].ConversationID != workerSession || after.Workers[0].Lifecycle != "running" {
		t.Fatal("reset changed team", after)
	}
	if len(after.Conversations) != 3 || after.Orchestrator.ConversationID != reset.ConversationID || after.Conversations[0].SessionRef == "" {
		t.Fatal("reset lost old session/history", after.Conversations)
	}
	if _, err := os.Stat(after.Workers[0].WorkingDir); err != nil {
		t.Fatal(err)
	}
	call(t, s, v.ID, "resume", "")
	fresh := next(t, f)
	if fresh.claim.Turn.MessageID != queued.MessageID || fresh.claim.SessionRef != "" || fresh.claim.Turn.ConversationID != reset.ConversationID {
		t.Fatal("queued input entered wrong conversation", fresh.claim)
	}
	for _, fact := range []string{worker.AgentID, after.Workers[0].WorkingDir, "kept", "running", "queued messages", "read_agent"} {
		if !strings.Contains(fresh.claim.Instructions, fact) {
			t.Fatalf("orientation lacks %q: %s", fact, fresh.claim.Instructions)
		}
	}
	fresh.done <- nil
	wr.done <- nil
	ready(t, db, v.ID, "ready")
	s.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.Snapshot(v.ID)
	if err != nil || len(saved.Conversations) != 3 || saved.Orchestrator.ConversationID != reset.ConversationID || len(saved.Workers) != 1 || len(saved.Turns) != 3 {
		t.Fatal("disk reopen lost reset/team", saved, err)
	}
}
