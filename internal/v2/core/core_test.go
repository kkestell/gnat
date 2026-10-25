package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/v2/store"
	"github.com/kkestell/gnat/internal/v2/workspace"
)

type run struct {
	claim *store.Claim
	done  chan error
	ended chan struct{}
}
type fake struct {
	runs      chan *run
	cleanup   <-chan struct{}
	cancelled chan struct{}
}

func (f *fake) Probe(context.Context, store.Settings) error { return nil }
func (f *fake) Run(ctx context.Context, c *store.Claim, emit func(map[string]any) error) error {
	r := &run{claim: c, done: make(chan error, 1), ended: make(chan struct{})}
	defer close(r.ended)
	if err := emit(map[string]any{"type": "session", "session_ref": "session-" + c.Agent.ID}); err != nil {
		return err
	}
	f.runs <- r
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		if f.cancelled != nil {
			f.cancelled <- struct{}{}
		}
		if f.cleanup != nil {
			<-f.cleanup
		}
		return ctx.Err()
	}
}
func ready(t *testing.T, db *store.Store, id, lifecycle string) store.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, err := db.Snapshot(id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Orchestrator.Lifecycle == lifecycle {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", lifecycle)
	return store.Snapshot{}
}
func next(t *testing.T, f *fake) *run {
	t.Helper()
	select {
	case r := <-f.runs:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no turn")
		return nil
	}
}
func call(t *testing.T, s *Service, id, kind, text string) store.Result {
	t.Helper()
	result, err := s.Mutate(id, store.NewID(), kind, text, "")
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func newWorkspace(t *testing.T, db *store.Store, root string) store.Snapshot {
	t.Helper()
	v, err := db.OpenWorkspace(workspace.Workspace{Root: root, Name: root}, func() (store.Settings, error) {
		return store.Settings{Harness: "claude", Model: "fixture", Effort: "high"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestQueueControlsAndRestart(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, "/repo")
	f := &fake{runs: make(chan *run, 10)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ready(t, db, v.ID, "ready")
	call(t, s, v.ID, "send", "one")
	r := next(t, f)
	call(t, s, v.ID, "send", "two")
	call(t, s, v.ID, "pause", "")
	r.done <- nil
	ready(t, db, v.ID, "ready")
	select {
	case <-f.runs:
		t.Fatal("paused queue ran")
	default:
	}
	call(t, s, v.ID, "resume", "")
	r = next(t, f)
	if r.claim.Text != "two" || r.claim.SessionRef != "session-"+v.Orchestrator.ID {
		t.Fatal("lost continuity", r.claim)
	}
	call(t, s, v.ID, "send", "three")
	key := store.NewID()
	target := r.claim.Turn.ID
	if _, err = s.Mutate(v.ID, key, "interrupt", "", target); err != nil {
		t.Fatal(err)
	}
	got := ready(t, db, v.ID, "interrupted")
	if got.Messages[1].State != "delivered" || got.Messages[2].State != "queued" {
		t.Fatal("interrupt replayed input")
	}
	call(t, s, v.ID, "resume", "")
	r = next(t, f)
	if _, err = s.Mutate(v.ID, key, "interrupt", "", target); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.ended:
		t.Fatal("retried interrupt cancelled a later turn")
	default:
	}
	s.Close()
	got = ready(t, db, v.ID, "interrupted")
	if len(got.Turns) != 3 {
		t.Fatal("wrong turns", got)
	}
	s2, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	call(t, s2, v.ID, "send", "four")
	call(t, s2, v.ID, "resume", "")
	r = next(t, f)
	if r.claim.Text != "four" {
		t.Fatal("replayed interrupted input")
	}
	r.done <- nil
	ready(t, db, v.ID, "ready")
	call(t, s2, v.ID, "reset", "")
	ready(t, db, v.ID, "ready")
	call(t, s2, v.ID, "send", "fresh")
	r = next(t, f)
	if r.claim.SessionRef != "" || r.claim.Turn.ConversationID == v.Orchestrator.ConversationID {
		t.Fatal("reset retained old session")
	}
	r.done <- nil
}
func TestSwarmIndependenceAndFailure(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := newWorkspace(t, db, "/a")
	b := newWorkspace(t, db, "/b")
	f := &fake{runs: make(chan *run, 10)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ready(t, db, a.ID, "ready")
	ready(t, db, b.ID, "ready")
	call(t, s, a.ID, "send", "a")
	ra := next(t, f)
	call(t, s, b.ID, "send", "b")
	rb := next(t, f)
	call(t, s, a.ID, "stop", "")
	ready(t, db, a.ID, "interrupted")
	select {
	case <-rb.ended:
		t.Fatal("other swarm interrupted")
	default:
	}
	<-ra.ended
	rb.done <- fmt.Errorf("saved session unavailable")
	ready(t, db, b.ID, "error")
	call(t, s, b.ID, "send", "follow-up")
	s.Open(b.ID)
	time.Sleep(20 * time.Millisecond)
	got, _ := db.Snapshot(b.ID)
	if got.Orchestrator.Lifecycle != "error" {
		t.Fatal("open hid session error")
	}
	call(t, s, b.ID, "retry", "")
	r := next(t, f)
	if r.claim.Text != "follow-up" || r.claim.SessionRef == "" {
		t.Fatal("retry replaced session")
	}
	r.done <- nil
}

func TestInterruptTargetsOnlyNamedTurn(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := newWorkspace(t, db, "/repo")
	f := &fake{runs: make(chan *run, 10)}
	s, err := New(db, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ready(t, db, v.ID, "ready")
	call(t, s, v.ID, "send", "one")
	first := next(t, f)
	call(t, s, v.ID, "send", "two")
	first.done <- nil
	second := next(t, f)
	if _, err = s.Mutate(v.ID, store.NewID(), "interrupt", "", first.claim.Turn.ID); err == nil {
		t.Fatal("accepted stale turn interruption")
	}
	current, err := db.Snapshot(v.ID)
	if err != nil || current.Scheduling != "running" || current.Turns[1].State != "running" {
		t.Fatal("stale interruption changed successor", current, err)
	}
	second.done <- nil
	ready(t, db, v.ID, "ready")
}
func TestResumeWaitsForInterruptionToSettle(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cleanup := make(chan struct{})
	released := false
	v := newWorkspace(t, db, "/repo")
	f := &fake{runs: make(chan *run, 10), cleanup: cleanup, cancelled: make(chan struct{}, 10)}
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
	ready(t, db, v.ID, "ready")
	call(t, s, v.ID, "send", "one")
	first := next(t, f)
	call(t, s, v.ID, "send", "two")
	if _, err = s.Mutate(v.ID, store.NewID(), "interrupt", "", first.claim.Turn.ID); err != nil {
		t.Fatal(err)
	}
	<-f.cancelled
	resumed := make(chan error, 1)
	go func() { _, err := s.Mutate(v.ID, store.NewID(), "resume", "", ""); resumed <- err }()
	select {
	case err := <-resumed:
		t.Fatal("resume acknowledged before cleanup", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(cleanup)
	released = true
	select {
	case err := <-resumed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume never settled")
	}
	second := next(t, f)
	if second.claim.Text != "two" {
		t.Fatal("wrong queued input")
	}
	second.done <- nil
	got := ready(t, db, v.ID, "ready")
	if got.Scheduling != "running" {
		t.Fatal("resume overwritten by interruption cleanup")
	}
}
