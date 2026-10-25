package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kkestell/gnat/internal/v2/workspace"
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
func TestMigrationFromMilestoneOne(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "gnat.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema + `INSERT INTO workspaces VALUES('w','/repo','/repo/.git','repo','today'); INSERT INTO swarms VALUES('s','w','a','running','today'); INSERT INTO agents VALUES('a','s','orchestrator','orchestrator','claude','model','high','pending','/repo',NULL,'c'); INSERT INTO conversations VALUES('c','a','claude',NULL,'today',NULL); INSERT INTO events(schema_version,swarm_id,agent_id,conversation_id,created_at,payload) VALUES(1,'s','a','c','today','{"type":"swarm_created"}');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := openTest(t, dir)
	v, err := s.Snapshot("s")
	if err != nil || v.ID != "s" || v.Orchestrator.ID != "a" || v.Orchestrator.ConversationID != "c" || len(v.Events) != 1 {
		t.Fatalf("migration %+v %v", v, err)
	}
	mutate(t, s, v.ID, "one", "send", "hello")
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
