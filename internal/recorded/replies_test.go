package recorded

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/protocol"
)

func writeRecords(t *testing.T, dir string, records ...any) {
	t.Helper()
	var data []byte
	for _, record := range records {
		line, err := protocol.Encode(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
	}
	if err := os.WriteFile(filepath.Join(dir, EventsFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func record(agent string, e events.Event) any {
	return map[string]any{"agent": agent, "event": e}
}

func task(sender any, midTurn bool) events.Event {
	return events.Event{"type": "swarm_message", "sender": sender, "mid_turn": midTurn}
}

func TestRepliesRecoverEarlierPlansAndMarkTaskSenders(t *testing.T) {
	dir := t.TempDir()
	plan := "Plan:\n  - Keep this indentation.\n\n```go\n  example()\n```"
	writeRecords(t, dir,
		record("alpha", task(nil, false)),
		record("alpha", events.NewMessage("progress text")),
		record("alpha", events.NewTurnEnd("nested reply", "").WithParent("tool1")),
		record("beta", task(nil, false)),
		record("beta", events.NewTurnEnd("PR #7", "")),
		record("alpha", events.NewToolResult("tool1", "tool output", false)),
		record("alpha", events.NewTurnEnd(plan, "")),
		record("alpha", events.Event{"type": "swarm_done", "exit": 0}),
		record("alpha", task("beta", false)),
		record("alpha", task(nil, true)), // A mid-turn correction doesn't change the task sender.
		record("alpha", events.NewTurnEnd("Already done.\n", "")),
		record("alpha", task(nil, false)),
		record("alpha", events.NewTurnEnd("", "")),
	)
	var out bytes.Buffer
	if err := WriteReplies(dir, []string{"alpha", "beta"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	want := "==> beta: turn 1 (reply to operator) <==\nPR #7\n\n" +
		"==> alpha: turn 1 (reply to operator) <==\n" + plan + "\n\n" +
		"==> alpha: turn 2 (reply to beta) <==\nAlready done.\n\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := WriteReplies(dir, []string{"alpha"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "PR #7") || !strings.Contains(out.String(), plan) {
		t.Fatalf("agent filter returned %q", out.String())
	}
}

func TestRepliesReadLongRecordsAndIgnoreAnUnfinishedAppend(t *testing.T) {
	dir := t.TempDir()
	reply := strings.Repeat("full reply\n  indented\n", 6000)
	writeRecords(t, dir, record("alpha", task(nil, false)), record("alpha", events.NewTurnEnd(reply, "")))
	f, err := os.OpenFile(filepath.Join(dir, EventsFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"agent":"alpha","event":`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var out bytes.Buffer
	if err := WriteReplies(dir, []string{"alpha"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if want := "==> alpha: turn 1 (reply to operator) <==\n" + reply + "\n"; out.String() != want {
		t.Fatalf("long reply was changed: got %d bytes, want %d", out.Len(), len(want))
	}
}

func TestRepliesExcludeARecreatedAgentsHistory(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE agent_ids (agent TEXT PRIMARY KEY, id TEXT); INSERT INTO agent_ids VALUES ('alpha', 'old')"); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, dir, record("alpha", task(nil, false)), record("alpha", events.NewTurnEnd("old work", "")))
	for _, tc := range []struct {
		id   string
		show bool
	}{{"old", true}, {"new", false}} {
		var out bytes.Buffer
		if err := WriteReplies(dir, []string{"alpha"}, map[string]string{"alpha": tc.id}, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "old work") != tc.show {
			t.Fatalf("ID %s: got %q", tc.id, out.String())
		}
	}
}

func TestRepliesWithoutAHistoryReturnNothing(t *testing.T) {
	var out bytes.Buffer
	if err := WriteReplies(t.TempDir(), []string{"alpha"}, nil, &out); err != nil || out.Len() != 0 {
		t.Fatalf("got %q, %v", out.String(), err)
	}
}

type brokenWriter struct{ err error }

func (w brokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRepliesReportReadAndWriteFailures(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, record("alpha", task(nil, false)), record("alpha", events.NewTurnEnd("reply", "")))
	failure := errors.New("broken output")
	if err := WriteReplies(dir, []string{"alpha"}, nil, brokenWriter{failure}); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, EventsFile), []byte("invalid record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteReplies(dir, []string{"alpha"}, nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "events.jsonl line 1") {
		t.Fatalf("got %v", err)
	}
}
