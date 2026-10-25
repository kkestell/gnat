package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Event struct {
	Sequence       int64          `json:"sequence"`
	SchemaVersion  int            `json:"schema_version"`
	SwarmID        string         `json:"swarm_id"`
	AgentID        string         `json:"agent_id"`
	ConversationID string         `json:"conversation_id"`
	TurnID         string         `json:"turn_id"`
	CreatedAt      string         `json:"created_at"`
	Payload        map[string]any `json:"payload"`
}
type Message struct {
	AgentID    string `json:"agent_id"`
	Sender     string `json:"sender"`
	ID         string `json:"id"`
	Text       string `json:"text"`
	State      string `json:"state"`
	AcceptedAt string `json:"accepted_at"`
}
type Turn struct {
	AgentID        string `json:"agent_id"`
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	State          string `json:"state"`
	Error          string `json:"error"`
}
type Conversation struct {
	AgentID    string `json:"agent_id"`
	ID         string `json:"id"`
	SessionRef string `json:"session_ref"`
	CreatedAt  string `json:"created_at"`
}
type Result struct {
	WorkingDir     string `json:"working_dir,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Membership     string `json:"membership,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	OK             bool   `json:"ok"`
	MessageID      string `json:"message_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}
type Claim struct {
	SwarmID      string
	Agent        Agent
	Turn         Turn
	Text         string
	SessionRef   string
	Sender       string
	MCPURL       string
	MCPToken     string
	Instructions string
	// Start is the turn_started sequence, where merging streamed text begins.
	Start int64
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func addEvent(tx *sql.Tx, id, aid, cid, tid string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO events(schema_version,swarm_id,agent_id,conversation_id,turn_id,created_at,payload) VALUES(1,?,?,?,?,?,?)`, id, aid, cid, tid, now(), string(data))
	return err
}

// fullSnapshot reads the swarm and its records, with its complete event history when events is set.
func fullSnapshot(tx *sql.Tx, id string, events bool) (Snapshot, error) {
	v, err := readSnapshot(tx, id)
	if err != nil {
		return v, err
	}
	all, err := readAgents(tx, id, true)
	v.Workers = []Agent{}
	v.RemovedWorkers = []Agent{}
	for _, a := range all {
		if a.Membership == "removed" {
			v.RemovedWorkers = append(v.RemovedWorkers, a)
		} else {
			v.Workers = append(v.Workers, a)
		}
	}
	if err != nil {
		return v, err
	}
	v.Events = []Event{}
	if events {
		if v.Events, err = readEvents(tx, id, 0); err != nil {
			return v, err
		}
	}
	v.Messages = []Message{}
	v.Turns = []Turn{}
	v.Conversations = []Conversation{}
	rows, err := tx.Query(`SELECT id,text,state,accepted_at,agent_id,sender FROM messages WHERE swarm_id=? ORDER BY acceptance_order`, id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Text, &m.State, &m.AcceptedAt, &m.AgentID, &m.Sender); err != nil {
			break
		}
		v.Messages = append(v.Messages, m)
	}
	if err = closeRows(rows, err); err != nil {
		return v, err
	}
	rows, err = tx.Query(`SELECT id,conversation_id,message_id,state,error,agent_id FROM turns WHERE swarm_id=? ORDER BY started_at,id`, id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var t Turn
		if err = rows.Scan(&t.ID, &t.ConversationID, &t.MessageID, &t.State, &t.Error, &t.AgentID); err != nil {
			break
		}
		v.Turns = append(v.Turns, t)
	}
	if err = closeRows(rows, err); err != nil {
		return v, err
	}
	rows, err = tx.Query(`SELECT id,coalesce(session_ref,''),created_at,agent_id FROM conversations WHERE agent_id IN (SELECT id FROM agents WHERE swarm_id=?) ORDER BY created_at,id`, id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var c Conversation
		if err = rows.Scan(&c.ID, &c.SessionRef, &c.CreatedAt, &c.AgentID); err != nil {
			break
		}
		v.Conversations = append(v.Conversations, c)
	}
	return v, closeRows(rows, err)
}
func closeRows(rows *sql.Rows, err error) error {
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return err
}
func readEvents(tx *sql.Tx, id string, after int64) ([]Event, error) {
	rows, err := tx.Query(`SELECT sequence,schema_version,swarm_id,coalesce(agent_id,''),coalesce(conversation_id,''),coalesce(turn_id,''),created_at,payload FROM events WHERE swarm_id=? AND sequence>? ORDER BY sequence`, id, after)
	if err != nil {
		return nil, err
	}
	out := []Event{}
	for rows.Next() {
		var e Event
		var raw string
		if err = rows.Scan(&e.Sequence, &e.SchemaVersion, &e.SwarmID, &e.AgentID, &e.ConversationID, &e.TurnID, &e.CreatedAt, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &e.Payload); err != nil {
			break
		}
		out = append(out, e)
	}
	return out, closeRows(rows, err)
}
func (s *Store) Events(id string, after int64) ([]Event, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := readEvents(tx, id, after)
	if err != nil {
		return nil, err
	}
	return v, tx.Commit()
}

// Mutate stores request results with their state changes. Replays have no effects.
func (s *Store) Mutate(id, requestID, kind, text, targetTurn string) (Result, bool, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 200 {
		return Result{}, false, fmt.Errorf("request_id is required (maximum 200 bytes)")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Result{}, false, err
	}
	defer tx.Rollback()
	inputJSON, _ := json.Marshal(map[string]string{"text": text, "turn_id": targetTurn})
	input := string(inputJSON)
	var oldKind, oldInput, raw string
	err = tx.QueryRow(`SELECT kind,input,result FROM operations WHERE swarm_id=? AND request_id=?`, id, requestID).Scan(&oldKind, &oldInput, &raw)
	if err == nil {
		if oldKind != kind || oldInput != input {
			return Result{}, false, fmt.Errorf("request_id already used for different input")
		}
		var result Result
		err = json.Unmarshal([]byte(raw), &result)
		return result, true, err
	}
	if err != sql.ErrNoRows {
		return Result{}, false, err
	}
	v, err := readSnapshot(tx, id)
	if err != nil {
		return Result{}, false, err
	}
	a := v.Orchestrator
	result := Result{OK: true}
	payload := map[string]any{"type": "scheduling_changed"}
	switch kind {
	case "send":
		if strings.TrimSpace(text) == "" {
			return Result{}, false, fmt.Errorf("text is required")
		}
		result.MessageID = NewID()
		_, err = tx.Exec(`INSERT INTO messages(id,swarm_id,agent_id,text,state,accepted_at,acceptance_order) VALUES(?,?,?,?,?,?,(SELECT coalesce(max(acceptance_order),0)+1 FROM messages))`, result.MessageID, id, a.ID, text, "queued", now())
		payload = map[string]any{"type": "message_accepted", "message_id": result.MessageID, "text": text, "sender": "user"}
	case "interrupt":
		var activeTurn string
		err = tx.QueryRow(`SELECT id FROM turns WHERE agent_id=? AND state='running'`, a.ID).Scan(&activeTurn)
		if targetTurn == "" || err == sql.ErrNoRows || (err == nil && activeTurn != targetTurn) {
			return Result{}, false, fmt.Errorf("the targeted turn is no longer running")
		}
		if err != nil {
			return Result{}, false, err
		}
		_, err = tx.Exec(`UPDATE swarms SET scheduling='paused' WHERE id=?`, id)
		payload["scheduling"] = "paused"
		payload["target_turn_id"] = targetTurn
	case "pause", "stop":
		scheduling := "paused"
		if kind == "stop" {
			scheduling = "stopped"
		}
		_, err = tx.Exec(`UPDATE swarms SET scheduling=? WHERE id=?`, scheduling, id)
		payload["scheduling"] = scheduling
	case "resume", "retry":
		_, err = tx.Exec(`UPDATE swarms SET scheduling='running' WHERE id=?`, id)
		payload["scheduling"] = "running"
	case "reset":
		if a.Lifecycle == "running" {
			return Result{}, false, fmt.Errorf("interrupt the running turn before starting a new conversation")
		}
		cid := NewID()
		result.ConversationID = cid
		if _, err = tx.Exec(`UPDATE conversations SET ended_at=? WHERE id=?`, now(), a.ConversationID); err == nil {
			_, err = tx.Exec(`INSERT INTO conversations VALUES(?,?,?,?,?,?)`, cid, a.ID, a.Settings.Harness, nil, now(), nil)
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE agents SET conversation_id=?,lifecycle='pending',error='' WHERE id=?`, cid, a.ID)
		}
		a.ConversationID = cid
		payload = map[string]any{"type": "conversation_created"}
	default:
		return Result{}, false, fmt.Errorf("unknown action %q", kind)
	}
	if err != nil {
		return Result{}, false, err
	}
	if err = addEvent(tx, id, a.ID, a.ConversationID, "", payload); err != nil {
		return Result{}, false, err
	}
	data, _ := json.Marshal(result)
	if _, err = tx.Exec(`INSERT INTO operations VALUES(?,?,?,?,?)`, id, requestID, kind, input, string(data)); err != nil {
		return Result{}, false, err
	}
	return result, false, tx.Commit()
}

func (s *Store) Ready(id string, probeErr error) error {
	v, err := s.Swarm(id)
	if err != nil {
		return err
	}
	return s.ReadyAgent(id, v.Orchestrator.ID, probeErr)
}
func (s *Store) ReadyAgent(id, aid string, probeErr error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := readAgent(tx, id, aid)
	if err != nil {
		return err
	}
	if a.Membership != "active" {
		return fmt.Errorf("agent is not active")
	}
	lifecycle, reason := "ready", ""
	if probeErr != nil {
		lifecycle, reason = "error", probeErr.Error()
	}
	if _, err = tx.Exec(`UPDATE agents SET lifecycle=?,error=? WHERE id=?`, lifecycle, reason, a.ID); err != nil {
		return err
	}
	if err = addEvent(tx, id, a.ID, a.ConversationID, "", map[string]any{"type": "agent_state", "lifecycle": lifecycle, "error": reason}); err != nil {
		return err
	}
	if a.Role == "worker" && probeErr != nil {
		if err = notify(tx, id, "Worker "+a.Name+" ("+a.ID+"): "+reason); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Claim(id string) (*Claim, error) {
	v, err := s.Swarm(id)
	if err != nil {
		return nil, err
	}
	return s.ClaimAgent(id, v.Orchestrator.ID)
}
func (s *Store) ClaimAgent(id, aid string) (*Claim, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := readSnapshot(tx, id)
	if err != nil {
		return nil, err
	}
	a, err := readAgent(tx, id, aid)
	if err != nil {
		return nil, err
	}
	if v.Scheduling != "running" || a.Lifecycle != "ready" || a.Membership != "active" {
		return nil, nil
	}
	var mid, text, sender string
	err = tx.QueryRow(`SELECT id,text,sender FROM messages WHERE agent_id=? AND state='queued' ORDER BY acceptance_order LIMIT 1`, aid).Scan(&mid, &text, &sender)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c := &Claim{SwarmID: id, Agent: a, Turn: Turn{AgentID: a.ID, ID: NewID(), ConversationID: a.ConversationID, MessageID: mid, State: "running"}, Text: text, Sender: sender}
	if err = tx.QueryRow(`SELECT coalesce(session_ref,'') FROM conversations WHERE id=?`, c.Turn.ConversationID).Scan(&c.SessionRef); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO turns(id,swarm_id,agent_id,conversation_id,message_id,state,started_at) VALUES(?,?,?,?,?,'running',?)`, c.Turn.ID, id, c.Agent.ID, c.Turn.ConversationID, mid, now()); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE messages SET state='delivered' WHERE id=?`, mid); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE agents SET lifecycle='running',error='' WHERE id=?`, c.Agent.ID); err != nil {
		return nil, err
	}
	if err = addEvent(tx, id, c.Agent.ID, c.Turn.ConversationID, c.Turn.ID, map[string]any{"type": "turn_started", "message_id": mid}); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(`SELECT last_insert_rowid()`).Scan(&c.Start); err != nil {
		return nil, err
	}
	return c, tx.Commit()
}
func (s *Store) Append(c *Claim, payload map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if payload["type"] == "session" {
		ref, ok := payload["session_ref"].(string)
		if !ok || ref == "" {
			return fmt.Errorf("empty harness session reference")
		}
		if c.SessionRef != "" && c.SessionRef != ref {
			return fmt.Errorf("harness resumed a different session")
		}
		if _, err = tx.Exec(`UPDATE conversations SET session_ref=? WHERE id=?`, ref, c.Turn.ConversationID); err != nil {
			return err
		}
	}
	if err = addEvent(tx, c.SwarmID, c.Agent.ID, c.Turn.ConversationID, c.Turn.ID, payload); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Finish(c *Claim, state, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE turns SET state=?,ended_at=?,error=? WHERE id=?`, state, now(), reason, c.Turn.ID); err != nil {
		return err
	}
	lifecycle := "ready"
	if state != "completed" {
		lifecycle = state
		if c.Agent.Role == "orchestrator" {
			if _, err = tx.Exec(`UPDATE swarms SET scheduling=CASE WHEN scheduling='stopped' THEN scheduling ELSE 'paused' END WHERE id=?`, c.SwarmID); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`UPDATE agents SET lifecycle=?,error=? WHERE id=?`, lifecycle, reason, c.Agent.ID); err != nil {
		return err
	}
	if c.Agent.Role == "worker" && state != "completed" {
		if err := notify(tx, c.SwarmID, "Worker "+c.Agent.Name+" ("+c.Agent.ID+"): "+state+": "+reason); err != nil {
			return err
		}
	}
	if err = mergeText(tx, c.SwarmID, c.Turn.ID, c.Start); err != nil {
		return err
	}
	if err = addEvent(tx, c.SwarmID, c.Agent.ID, c.Turn.ConversationID, c.Turn.ID, map[string]any{"type": "turn_ended", "state": state, "error": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// Recover terminates persisted running turns without putting delivered input back.
// Outcomes match Finish: stopped swarms stay stopped and interrupted workers notify the orchestrator.
func (s *Store) Recover() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT t.id,t.swarm_id,t.agent_id,t.conversation_id,a.role,a.name,(SELECT coalesce(min(sequence),0) FROM events WHERE swarm_id=t.swarm_id AND turn_id=t.id) FROM turns t JOIN agents a ON a.id=t.agent_id WHERE t.state='running'`)
	if err != nil {
		return err
	}
	type interrupted struct {
		id, swarm, agent, conversation, role, name string
		start                                      int64
	}
	var all []interrupted
	for rows.Next() {
		var v interrupted
		if err = rows.Scan(&v.id, &v.swarm, &v.agent, &v.conversation, &v.role, &v.name, &v.start); err != nil {
			break
		}
		all = append(all, v)
	}
	if err = closeRows(rows, err); err != nil {
		return err
	}
	const reason = "Daemon restarted during execution"
	for _, v := range all {
		if _, err = tx.Exec(`UPDATE turns SET state='interrupted',ended_at=?,error=? WHERE id=?`, now(), reason, v.id); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE agents SET lifecycle='interrupted',error=? WHERE id=?`, reason, v.agent); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE swarms SET scheduling=CASE WHEN scheduling='stopped' THEN scheduling ELSE 'paused' END WHERE id=?`, v.swarm); err != nil {
			return err
		}
		if v.role == "worker" {
			if err = notify(tx, v.swarm, "Worker "+v.name+" ("+v.agent+"): interrupted: "+reason); err != nil {
				return err
			}
		}
		if err = mergeText(tx, v.swarm, v.id, v.start); err != nil {
			return err
		}
		if err = addEvent(tx, v.swarm, v.agent, v.conversation, v.id, map[string]any{"type": "turn_ended", "state": "interrupted", "error": reason}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// mergeText replaces each run of a turn's streamed text_delta events that share a
// parent_tool_use_id with one message event at the run's first sequence.
func mergeText(tx *sql.Tx, swarm, turn string, from int64) error {
	rows, err := tx.Query(`SELECT sequence,payload FROM events WHERE swarm_id=? AND sequence>=? AND turn_id=? ORDER BY sequence`, swarm, from, turn)
	if err != nil {
		return err
	}
	type run struct {
		first  int64
		rest   []int64
		parent string
		text   strings.Builder
	}
	var runs []*run
	var open *run
	for rows.Next() {
		var sequence int64
		var raw string
		if err = rows.Scan(&sequence, &raw); err != nil {
			break
		}
		var payload map[string]any
		if err = json.Unmarshal([]byte(raw), &payload); err != nil {
			break
		}
		if payload["type"] != "text_delta" {
			open = nil
			continue
		}
		parent, _ := payload["parent_tool_use_id"].(string)
		text, _ := payload["text"].(string)
		if open != nil && open.parent == parent {
			open.rest = append(open.rest, sequence)
		} else {
			open = &run{first: sequence, parent: parent}
			runs = append(runs, open)
		}
		open.text.WriteString(text)
	}
	if err = closeRows(rows, err); err != nil {
		return err
	}
	for _, r := range runs {
		payload := map[string]any{"type": "message", "text": r.text.String()}
		if r.parent != "" {
			payload["parent_tool_use_id"] = r.parent
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE events SET payload=? WHERE sequence=?`, string(data), r.first); err != nil {
			return err
		}
		for _, sequence := range r.rest {
			if _, err = tx.Exec(`DELETE FROM events WHERE sequence=?`, sequence); err != nil {
				return err
			}
		}
	}
	return nil
}
