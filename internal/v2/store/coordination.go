package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const migration3 = `
ALTER TABLE agents ADD COLUMN membership TEXT NOT NULL DEFAULT 'active';
ALTER TABLE agents ADD COLUMN stage TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN base_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE swarms ADD COLUMN worker_defaults TEXT NOT NULL DEFAULT '{}';
ALTER TABLE messages ADD COLUMN sender TEXT NOT NULL DEFAULT 'user';
CREATE INDEX messages_agent_queue ON messages(agent_id,state,acceptance_order);
PRAGMA user_version = 3;
`

const agentColumns = `id,role,name,harness,model,effort,lifecycle,working_dir,branch,conversation_id,error,membership,stage,base_commit,coalesce((SELECT id FROM turns WHERE agent_id=agents.id AND state='running' LIMIT 1),'')`

func scanAgent(row interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	err := row.Scan(&a.ID, &a.Role, &a.Name, &a.Settings.Harness, &a.Settings.Model, &a.Settings.Effort, &a.Lifecycle, &a.WorkingDir, &a.Branch, &a.ConversationID, &a.Error, &a.Membership, &a.Stage, &a.BaseCommit, &a.TurnID)
	return a, err
}
func readAgent(q queryer, swarm, aid string) (Agent, error) {
	return scanAgent(q.QueryRow(`SELECT `+agentColumns+` FROM agents WHERE swarm_id=? AND id=?`, swarm, aid))
}
func readAgents(tx *sql.Tx, swarm string, removed bool) ([]Agent, error) {
	query := `SELECT ` + agentColumns + ` FROM agents WHERE swarm_id=? AND role='worker'`
	if !removed {
		query += ` AND membership!='removed'`
	}
	rows, err := tx.Query(query+` ORDER BY rowid`, swarm)
	if err != nil {
		return nil, err
	}
	out := []Agent{}
	for rows.Next() {
		a, e := scanAgent(rows)
		if e != nil {
			err = e
			break
		}
		out = append(out, a)
	}
	return out, closeRows(rows, err)
}
func (s *Store) Agent(swarm, aid string) (Agent, error) { return readAgent(s.db, swarm, aid) }
func (s *Store) Agents(swarm string, removed bool) ([]Agent, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := readSnapshot(tx, swarm)
	if err != nil {
		return nil, err
	}
	workers, err := readAgents(tx, swarm, removed)
	if err != nil {
		return nil, err
	}
	return append([]Agent{v.Orchestrator}, workers...), tx.Commit()
}

// Command has no caller or swarm fields: transports supply an authenticated binding.
type Command struct {
	RequestID string `json:"request_id"`
	Name      string `json:"name,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	Harness   string `json:"harness,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Base      string `json:"base,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
}

func requestKey(caller, request string) string { return "agent:" + caller + ":" + request }
func operation(tx *sql.Tx, swarm, key, kind, input string) (Result, bool, error) {
	var oldKind, oldInput, data string
	err := tx.QueryRow(`SELECT kind,input,result FROM operations WHERE swarm_id=? AND request_id=?`, swarm, key).Scan(&oldKind, &oldInput, &data)
	if err == sql.ErrNoRows {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	if kind != oldKind || input != oldInput {
		return Result{}, false, fmt.Errorf("request_id was already used for different input")
	}
	var r Result
	err = json.Unmarshal([]byte(data), &r)
	return r, true, err
}
func saveOperation(tx *sql.Tx, swarm, key, kind, input string, r Result) error {
	data, _ := json.Marshal(r)
	_, err := tx.Exec(`INSERT INTO operations VALUES(?,?,?,?,?)`, swarm, key, kind, input, string(data))
	return err
}
func (s *Store) Replay(swarm, caller, kind string, c Command) (Result, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Result{}, false, err
	}
	defer tx.Rollback()
	data, _ := json.Marshal(c)
	return operation(tx, swarm, requestKey(caller, c.RequestID), kind, string(data))
}
func enqueue(tx *sql.Tx, swarm, recipient, sender, text string) (string, error) {
	a, err := readAgent(tx, swarm, recipient)
	if err != nil {
		return "", fmt.Errorf("unknown recipient: %w", err)
	}
	if a.Membership == "removed" || a.Membership == "removing" {
		return "", fmt.Errorf("recipient is %s", a.Membership)
	}
	mid := NewID()
	_, err = tx.Exec(`INSERT INTO messages(id,swarm_id,agent_id,text,state,accepted_at,acceptance_order,sender) VALUES(?,?,?,?,?,?,(SELECT coalesce(max(acceptance_order),0)+1 FROM messages),?)`, mid, swarm, recipient, text, "queued", now(), sender)
	if err == nil {
		err = addEvent(tx, swarm, recipient, a.ConversationID, "", map[string]any{"type": "message_accepted", "message_id": mid, "text": text, "sender": sender})
	}
	return mid, err
}
func notify(tx *sql.Tx, swarm, text string) error {
	v, err := readSnapshot(tx, swarm)
	if err != nil {
		return err
	}
	_, err = enqueue(tx, swarm, v.Orchestrator.ID, "system", text)
	return err
}

// Coordinate commits authorization, operation result, messages and lifecycle events together.
func (s *Store) Coordinate(swarm, caller, kind string, c Command, base string, settings Settings) (Result, bool, error) {
	if strings.TrimSpace(c.RequestID) == "" || len(c.RequestID) > 200 {
		return Result{}, false, fmt.Errorf("request_id is required (maximum 200 bytes)")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Result{}, false, err
	}
	defer tx.Rollback()
	a, err := readAgent(tx, swarm, caller)
	if err != nil {
		return Result{}, false, err
	}
	if a.Membership != "active" {
		return Result{}, false, fmt.Errorf("caller is not active")
	}
	if kind != "send_message" && a.Role != "orchestrator" {
		return Result{}, false, fmt.Errorf("only the orchestrator may manage workers")
	}
	data, _ := json.Marshal(c)
	input := string(data)
	key := requestKey(caller, c.RequestID)
	if r, replay, e := operation(tx, swarm, key, kind, input); e != nil || replay {
		return r, replay, e
	}
	r := Result{OK: true, AgentID: c.AgentID}
	switch kind {
	case "send_message":
		if strings.TrimSpace(c.Text) == "" {
			return r, false, fmt.Errorf("text is required")
		}
		r.MessageID, err = enqueue(tx, swarm, c.AgentID, caller, c.Text)
	case "create_agent":
		if strings.TrimSpace(c.Name) == "" || len(c.Name) > 120 || strings.TrimSpace(c.Prompt) == "" {
			return r, false, fmt.Errorf("name (maximum 120 bytes) and prompt are required")
		}
		if base == "" {
			return r, false, fmt.Errorf("resolved base commit is required")
		}
		aid, cid := NewID(), NewID()
		r.AgentID = aid
		branch := "gnat/" + swarm + "/" + aid
		dir := filepath.Join(s.Dir, "swarms", swarm, "agents", aid, "worktree")
		r.WorkingDir, r.Branch, r.Membership = dir, branch, "provisioning"
		_, err = tx.Exec(`INSERT INTO agents(id,swarm_id,role,name,harness,model,effort,lifecycle,working_dir,branch,conversation_id,membership,stage,base_commit) VALUES(?,?,'worker',?,?,?,?, 'pending',?,?,?,'provisioning','reserved',?)`, aid, swarm, c.Name, settings.Harness, settings.Model, settings.Effort, dir, branch, cid, base)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO conversations VALUES(?,?,?,?,?,?)`, cid, aid, settings.Harness, nil, now(), nil)
		}
		if err == nil {
			r.MessageID, err = enqueue(tx, swarm, aid, caller, c.Prompt)
		}
		if err == nil {
			err = addEvent(tx, swarm, aid, cid, "", map[string]any{"type": "agent_created", "name": c.Name, "branch": branch, "working_dir": dir})
		}
	case "interrupt_agent", "remove_agent":
		target, e := readAgent(tx, swarm, c.AgentID)
		if e != nil {
			return r, false, fmt.Errorf("unknown worker: %w", e)
		}
		if target.Role != "worker" || target.Membership == "removed" {
			return r, false, fmt.Errorf("target is not a current worker")
		}
		if kind == "interrupt_agent" {
			var tid string
			err = tx.QueryRow(`SELECT id FROM turns WHERE agent_id=? AND state='running'`, c.AgentID).Scan(&tid)
			if err != nil || c.TurnID == "" || c.TurnID != tid {
				return r, false, fmt.Errorf("the targeted turn is no longer running")
			}
		} else {
			_, err = tx.Exec(`UPDATE agents SET membership='removing',error='' WHERE id=?`, c.AgentID)
			if err == nil {
				err = cancelQueue(tx, swarm, target)
			}
			if err == nil {
				err = addEvent(tx, swarm, target.ID, target.ConversationID, "", map[string]any{"type": "agent_removing"})
			}
		}
	default:
		return r, false, fmt.Errorf("unknown coordination operation %q", kind)
	}
	if err != nil {
		return r, false, err
	}
	if err = saveOperation(tx, swarm, key, kind, input, r); err != nil {
		return r, false, err
	}
	return r, false, tx.Commit()
}
func cancelQueue(tx *sql.Tx, swarm string, a Agent) error {
	rows, err := tx.Query(`SELECT id FROM messages WHERE agent_id=? AND state='queued'`, a.ID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = closeRows(rows, err); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(`UPDATE messages SET state='cancelled' WHERE id=?`, id); err != nil {
			return err
		}
		if err = addEvent(tx, swarm, a.ID, a.ConversationID, "", map[string]any{"type": "message_cancelled", "message_id": id}); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) WorkerState(swarm, aid, membership, stage, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := readAgent(tx, swarm, aid)
	if err != nil {
		return err
	}
	if a.Membership == "removing" && membership != "removing" && membership != "removed" {
		return fmt.Errorf("worker is being removed")
	}
	_, err = tx.Exec(`UPDATE agents SET membership=?,stage=?,error=? WHERE id=?`, membership, stage, reason, aid)
	if err != nil {
		return err
	}
	if err = addEvent(tx, swarm, aid, a.ConversationID, "", map[string]any{"type": "agent_state", "membership": membership, "stage": stage, "error": reason}); err != nil {
		return err
	}
	if reason != "" {
		if err = notify(tx, swarm, "Worker "+a.Name+" ("+aid+") "+membership+" at "+stage+": "+reason); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReadAgent returns bounded recent events, including history of removed workers.
func (s *Store) ReadAgent(swarm, aid string, before int64, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	if before <= 0 {
		before = 1 << 62
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := readAgent(tx, swarm, aid)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT sequence,payload,coalesce(conversation_id,''),coalesce(turn_id,'') FROM events WHERE swarm_id=? AND agent_id=? AND sequence<? ORDER BY sequence DESC LIMIT ?`, swarm, aid, before, limit)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var next int64
	for rows.Next() {
		var seq int64
		var raw, cid, tid string
		if err = rows.Scan(&seq, &raw, &cid, &tid); err != nil {
			break
		}
		var payload map[string]any
		if err = json.Unmarshal([]byte(raw), &payload); err != nil {
			break
		}
		if len(raw) > 8000 {
			payload = map[string]any{"type": payload["type"], "preview": raw[:8000], "truncated": true}
		}
		out = append(out, map[string]any{"sequence": seq, "conversation_id": cid, "turn_id": tid, "payload": payload})
		next = seq
	}
	if err = closeRows(rows, err); err != nil {
		return nil, err
	}
	return map[string]any{"agent": a, "events": out, "before": next}, tx.Commit()
}
