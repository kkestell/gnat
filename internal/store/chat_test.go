package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/kkestell/gnat/internal/workspace"
)

func chatWorkspace(t *testing.T, s *Store, root string) Snapshot {
	t.Helper()
	v, err := s.OpenWorkspace(workspace.Workspace{Root: root, Name: root}, settings)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func mutate(t *testing.T, s *Store, id, key, kind, text string) Result {
	t.Helper()
	v, _, err := s.Mutate(id, key, kind, text, "")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestChatTransitionsAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	v := chatWorkspace(t, s, "/repo")
	other := chatWorkspace(t, s, "/other")
	first := mutate(t, s, v.ID, "one", "send", "first")
	second := mutate(t, s, v.ID, "two", "send", "second")
	replay, repeated, err := s.Mutate(v.ID, "one", "send", "first", "")
	if err != nil || !repeated || replay != first {
		t.Fatal("bad replay", replay, repeated, err)
	}
	if _, _, err = s.Mutate(v.ID, "one", "send", "changed", ""); err == nil {
		t.Fatal("accepted request collision")
	}
	if c, err := s.Claim(v.ID); err != nil || c != nil {
		t.Fatal("claimed before ready")
	}
	if err = s.Ready(v.ID, nil); err != nil {
		t.Fatal(err)
	}
	c, err := s.Claim(v.ID)
	if err != nil || c == nil || c.Turn.MessageID != first.MessageID {
		t.Fatal("FIFO claim", c, err)
	}
	if again, err := s.Claim(v.ID); err != nil || again != nil {
		t.Fatal("overlapping turn")
	}
	if _, _, err = s.Mutate(v.ID, "reset-active", "reset", "", ""); err == nil {
		t.Fatal("reset active turn")
	}
	if err = s.Append(c, map[string]any{"type": "session", "session_ref": "exact-session"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Append(c, map[string]any{"type": "text_delta", "text": "partial"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, dir)
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Snapshot(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scheduling != "paused" || got.Turns[0].State != "interrupted" || got.Messages[0].State != "delivered" || got.Messages[1].State != "queued" {
		t.Fatalf("recovery %+v", got)
	}
	if got.Conversations[0].SessionRef != "exact-session" {
		t.Fatal("lost exact session")
	}
	if err = s.Ready(v.ID, nil); err != nil {
		t.Fatal(err)
	}
	mutate(t, s, v.ID, "resume", "resume", "")
	c, err = s.Claim(v.ID)
	if err != nil || c == nil || c.Turn.MessageID != second.MessageID || c.SessionRef != "exact-session" {
		t.Fatalf("resume %+v %v", c, err)
	}
	if err = s.Append(c, map[string]any{"type": "session", "session_ref": "different"}); err == nil {
		t.Fatal("session replacement accepted")
	}
	if err = s.Finish(c, "completed", ""); err != nil {
		t.Fatal(err)
	}
	reset := mutate(t, s, v.ID, "reset", "reset", "")
	got, err = s.Snapshot(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Orchestrator.ConversationID != reset.ConversationID || len(got.Conversations) != 2 || got.Conversations[0].SessionRef != "exact-session" || len(got.Turns) != 2 {
		t.Fatal("reset lost history", got)
	}
	isolated, err := s.Events(other.ID, 0)
	if err != nil || len(isolated) != 4 {
		t.Fatal("cross-swarm events", isolated, err)
	}
	all, err := s.Events(v.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Events, all) {
		t.Fatal("snapshot events differ")
	}
	for _, e := range all {
		if e.Sequence > got.Cursor {
			t.Fatal("snapshot cursor precedes committed event")
		}
	}
	after, err := s.Events(v.ID, got.Cursor)
	if err != nil || len(after) != 0 {
		t.Fatal("replay cursor", err)
	}
}
func TestMutationRollback(t *testing.T) {
	s := openTest(t, t.TempDir())
	v := chatWorkspace(t, s, "/repo")
	if _, err := s.db.Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'disk failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Mutate(v.ID, "one", "send", "hello", ""); err == nil {
		t.Fatal("acknowledged failed write")
	}
	got, err := s.Snapshot(v.ID)
	if err != nil || len(got.Messages) != 0 || got.Cursor != v.Cursor {
		t.Fatal("partial transaction", got, err)
	}
	if err = s.Ready(v.ID, errors.New("missing auth")); err == nil {
		t.Fatal("readiness hid failed write")
	}
}

// runningWorker creates an active worker and claims its initial prompt.
func runningWorker(t *testing.T, s *Store, v Snapshot, name string) *Claim {
	t.Helper()
	settings, _ := settings()
	r, _, err := s.Coordinate(v.ID, v.Orchestrator.ID, "create_agent", Command{RequestID: name, Name: name, Prompt: "work"}, "base", settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WorkerState(v.ID, r.AgentID, "active", "worktree_created", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.ReadyAgent(v.ID, r.AgentID, nil); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimAgent(v.ID, r.AgentID)
	if err != nil || c == nil {
		t.Fatal("claim worker", c, err)
	}
	return c
}
func appendAll(t *testing.T, s *Store, c *Claim, events ...map[string]any) {
	t.Helper()
	for _, e := range events {
		if err := s.Append(c, e); err != nil {
			t.Fatal(err)
		}
	}
}
func turnEvents(t *testing.T, s *Store, swarm, turn string) []map[string]any {
	t.Helper()
	all, err := s.Events(swarm, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range all {
		if e.TurnID == turn {
			out = append(out, e.Payload)
		}
	}
	return out
}

func TestRecoveryNotifiesOrchestratorAndKeepsStoppedSwarms(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	paused := chatWorkspace(t, s, "/paused")
	stopped := chatWorkspace(t, s, "/stopped")
	worker := runningWorker(t, s, paused, "builder")
	runningWorker(t, s, stopped, "tester")
	mutate(t, s, stopped.ID, "stop", "stop", "")
	queuedBefore, err := s.Queued(paused.Orchestrator.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.Recover(); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := s.Swarm(paused.ID); v.Scheduling != "paused" {
		t.Fatal("recovery did not pause", v.Scheduling)
	}
	if v, _ := s.Swarm(stopped.ID); v.Scheduling != "stopped" {
		t.Fatal("recovery replaced stopped scheduling", v.Scheduling)
	}
	got, err := s.State(paused.ID)
	if err != nil {
		t.Fatal(err)
	}
	var notices []Message
	for _, m := range got.Messages {
		if m.AgentID == paused.Orchestrator.ID && m.Sender == "system" {
			notices = append(notices, m)
		}
	}
	queued, _ := s.Queued(paused.Orchestrator.ID)
	if len(notices) != 1 || queued != queuedBefore+1 || notices[0].Text != "Worker builder ("+worker.Agent.ID+"): interrupted: Daemon restarted during execution" {
		t.Fatalf("recovery notification %+v queued %d", notices, queued)
	}
	for _, m := range got.Messages {
		if m.ID == worker.Turn.MessageID && m.State != "delivered" {
			t.Fatal("interrupted worker prompt was requeued", m)
		}
	}
}

func TestStreamedTextMergesAtTurnEnd(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	v := chatWorkspace(t, s, "/repo")
	c := runningWorker(t, s, v, "builder")
	appendAll(t, s, c,
		map[string]any{"type": "text_delta", "text": "Hello "},
		map[string]any{"type": "text_delta", "text": "world"},
		map[string]any{"type": "tool_call", "id": "tool", "name": "Bash", "input": map[string]any{}},
		map[string]any{"type": "text_delta", "text": "nested", "parent_tool_use_id": "tool"},
		map[string]any{"type": "text_delta", "text": " text", "parent_tool_use_id": "tool"},
	)
	before, _ := s.Events(v.ID, 0)
	var toolSequence int64
	for _, e := range before {
		if e.Payload["type"] == "tool_call" {
			toolSequence = e.Sequence
		}
	}
	if err := s.Finish(c, "completed", ""); err != nil {
		t.Fatal(err)
	}
	events := turnEvents(t, s, v.ID, c.Turn.ID)
	want := []map[string]any{
		{"type": "turn_started", "message_id": c.Turn.MessageID},
		{"type": "message", "text": "Hello world"},
		{"type": "tool_call", "id": "tool", "name": "Bash", "input": map[string]any{}},
		{"type": "message", "text": "nested text", "parent_tool_use_id": "tool"},
		{"type": "turn_ended", "state": "completed", "error": ""},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("merged events %v", events)
	}
	after, _ := s.Events(v.ID, 0)
	for _, e := range after {
		if e.Payload["type"] == "tool_call" && e.Sequence != toolSequence {
			t.Fatal("merging moved a tool call")
		}
	}
	history, err := s.ReadAgent(v.ID, c.Agent.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	recent := history["events"].([]map[string]any)
	if recent[1]["payload"].(map[string]any)["text"] != "nested text" {
		t.Fatal("read_agent returned fragments", recent)
	}

	// An interrupted turn merges during recovery.
	if err = s.WorkerState(v.ID, c.Agent.ID, "active", "worktree_created", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Coordinate(v.ID, v.Orchestrator.ID, "send_message", Command{RequestID: "again", AgentID: c.Agent.ID, Text: "again"}, "", Settings{}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReadyAgent(v.ID, c.Agent.ID, nil); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimAgent(v.ID, c.Agent.ID)
	if err != nil || next == nil {
		t.Fatal("claim", next, err)
	}
	appendAll(t, s, next, map[string]any{"type": "text_delta", "text": "par"}, map[string]any{"type": "text_delta", "text": "tial"})
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	events = turnEvents(t, s, v.ID, next.Turn.ID)
	if len(events) != 3 || events[1]["type"] != "message" || events[1]["text"] != "partial" {
		t.Fatalf("recovered events %v", events)
	}
}

func TestSwarmAndStateReadsOmitHistory(t *testing.T) {
	s := openTest(t, t.TempDir())
	v := chatWorkspace(t, s, "/repo")
	mutate(t, s, v.ID, "one", "send", "first")
	runningWorker(t, s, v, "builder")
	record, err := s.Swarm(v.ID)
	if err != nil || record.Orchestrator.ID != v.Orchestrator.ID || record.Events != nil || record.Messages != nil || record.Workers != nil {
		t.Fatalf("swarm record %+v %v", record, err)
	}
	state, err := s.State(v.ID)
	if err != nil || len(state.Events) != 0 || len(state.Messages) != 2 || len(state.Turns) != 1 || len(state.Workers) != 1 || len(state.Conversations) != 2 {
		t.Fatalf("state %+v %v", state, err)
	}
	full, _ := s.Snapshot(v.ID)
	if len(full.Events) == 0 || full.Cursor != state.Cursor {
		t.Fatal("snapshot and state disagree")
	}
	if n, _ := s.Queued(v.Orchestrator.ID); n != 1 {
		t.Fatal("queued orchestrator input", n)
	}
	if n, _ := s.Queued(state.Workers[0].ID); n != 0 {
		t.Fatal("delivered worker input counted as queued", n)
	}
	if _, err = s.Swarm("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing swarm", err)
	}
}
