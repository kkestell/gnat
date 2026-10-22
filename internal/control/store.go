package control

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/recorded"
	_ "modernc.org/sqlite" // The database driver.
)

// schema is control.db's, the same as the Python version's so either can read the file.
const schema = `
CREATE TABLE IF NOT EXISTS messages (
    position INTEGER PRIMARY KEY, agent TEXT NOT NULL, urgent INTEGER NOT NULL,
    text TEXT NOT NULL, sender TEXT, new INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS statuses (
    agent TEXT PRIMARY KEY, busy INTEGER NOT NULL DEFAULT 0, turns INTEGER NOT NULL DEFAULT 0,
    exit INTEGER, reply TEXT, error TEXT, stopped INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS usage (
    agent TEXT PRIMARY KEY, input_tokens INTEGER, cache_read_tokens INTEGER,
    cache_write_tokens INTEGER, output_tokens INTEGER, cost REAL);
`

// Connect opens the database at path (":memory:" for a throwaway one) and creates its
// tables.
func Connect(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection serializes access, and keeps an in-memory database in one piece.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// A Message is one queued for an agent.
type Message struct {
	Text string
	// Sender is the agent that sent it; empty when the operator did.
	Sender string
	// New starts a fresh session; only a message that starts a turn can.
	New bool
}

// LogLine is the message as the agent's log shows it arriving.
func (m Message) LogLine(midTurn bool) string {
	label := ">>> "
	if midTurn {
		label = ">>> (mid-turn) "
	} else if m.New {
		label = ">>> (new session) "
	}
	from := ""
	if m.Sender != "" {
		from = "[from " + m.Sender + "] "
	}
	return label + from + m.Text + "\n"
}

// AsEvent is the control plane's own swarm_message event for the message.
func (m Message) AsEvent(midTurn bool) events.Event {
	var sender any
	if m.Sender != "" {
		sender = m.Sender
	}
	return events.Event{"type": "swarm_message", "text": m.Text, "sender": sender,
		"new": m.New, "mid_turn": midTurn}
}

// ForAgent is what the agent's turn starts with.
func (m Message) ForAgent() string {
	if m.Sender != "" {
		return fmt.Sprintf("Message from agent %s:\n\n%s", m.Sender, m.Text)
	}
	return m.Text
}

// A Mailbox is an agent's pending messages, oldest first.
//
// Messages sent with now are delivered into the agent's running turn at its next tool
// call; any still pending when the turn ends start the next turn, ahead of the regular
// queue. A message sent with new starts a fresh session, which only a message that
// starts a turn can do, so it can't also be now.
type Mailbox struct {
	db    *sql.DB
	agent string

	mu      sync.Mutex
	changed chan struct{} // closed, and replaced, whenever a message is put
}

// NewMailbox is the agent's mailbox in db.
func NewMailbox(db *sql.DB, agent string) *Mailbox {
	return &Mailbox{db: db, agent: agent, changed: make(chan struct{})}
}

// Put queues a message: after the others, or before them all if front.
func (m *Mailbox) Put(msg Message, now, front bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A null position goes after every other; MIN is null when there are none.
	var position sql.NullInt64
	if front {
		if err := tx.QueryRow("SELECT MIN(position) - 1 FROM messages").Scan(&position); err != nil {
			return err
		}
	}
	var pos, sender any
	if position.Valid {
		pos = position.Int64
	}
	if msg.Sender != "" {
		sender = msg.Sender
	}
	if _, err := tx.Exec("INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?)",
		pos, m.agent, b2i(now), msg.Text, sender, b2i(msg.New)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Wake every waiter: one that was cancelled could swallow a single wakeup.
	close(m.changed)
	m.changed = make(chan struct{})
	return nil
}

// pop removes and returns the next message to start a turn with.
func (m *Mailbox) pop() (msg Message, urgent, ok bool, err error) {
	tx, err := m.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	var position int64
	var sender sql.NullString
	var isNew, isUrgent int
	err = tx.QueryRow("SELECT position, text, sender, new, urgent FROM messages WHERE agent = ? "+
		"ORDER BY urgent DESC, position LIMIT 1", m.agent).Scan(&position, &msg.Text, &sender, &isNew, &isUrgent)
	if err == sql.ErrNoRows {
		return msg, false, false, nil
	} else if err != nil {
		return
	}
	if _, err = tx.Exec("DELETE FROM messages WHERE position = ?", position); err != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	msg.Sender, msg.New = sender.String, isNew != 0
	return msg, isUrgent != 0, true, nil
}

// Get is the next message to start a turn with, and whether it was sent with now. It
// waits for one until done is closed; the error is then ErrCancelled. A message it
// returns has been taken even if done closed meanwhile. If taken isn't nil it runs once
// the message is out of the queue, before anything can see the queue without it, so the
// caller can show the message in progress with no moment when it is nowhere.
func (m *Mailbox) Get(done <-chan struct{}, taken func()) (Message, bool, error) {
	for {
		m.mu.Lock()
		changed := m.changed
		msg, urgent, ok, err := m.pop()
		if ok && taken != nil {
			taken()
		}
		m.mu.Unlock()
		if err != nil {
			return Message{}, false, err
		}
		if ok {
			return msg, urgent, nil
		}
		select {
		case <-changed:
		case <-done:
			return Message{}, false, ErrCancelled
		}
	}
}

// TakeUrgent removes and returns every message waiting to go into the running turn.
func (m *Mailbox) TakeUrgent() ([]Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT text, sender FROM messages WHERE agent = ? AND urgent ORDER BY position", m.agent)
	if err != nil {
		return nil, err
	}
	var taken []Message
	for rows.Next() {
		var msg Message
		var sender sql.NullString
		if err := rows.Scan(&msg.Text, &sender); err != nil {
			rows.Close()
			return nil, err
		}
		msg.Sender = sender.String
		taken = append(taken, msg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM messages WHERE agent = ? AND urgent", m.agent); err != nil {
		return nil, err
	}
	return taken, tx.Commit()
}

// Pending is how many messages are queued.
func (m *Mailbox) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM messages WHERE agent = ?", m.agent).Scan(&n); err != nil {
		return 0
	}
	return n
}

// Queue is what is waiting in the queue: how many, how many were sent with now, and how
// many each teammate sent.
func (m *Mailbox) Queue() recorded.Queue {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, err := recorded.LoadQueue(m.db, m.agent)
	if err != nil {
		return recorded.Queue{}
	}
	return q
}

// Status is where an agent is in its work, for `gnat status` and `gnat wait`.
//
// All but Started are kept in the database: an agent that was busy when the swarm went
// down is still busy when it comes back up, until its daemon attaches and the turn is
// reported interrupted. The control plane guards it with its own lock.
type Status struct {
	db    *sql.DB
	agent string
	// Started is whether the agent's daemon has attached yet; until then it's cloning.
	Started bool
	Busy    bool
	Turns   int
	// Exit is the last finished turn's exit status, nil if the agent restarted mid-turn.
	Exit *int
	// Reply is the harness's final reply, nil if it never gave one; Error is why the
	// harness said the turn failed, if it did.
	Reply, Error *string
	// Stopped is whether the last turn was ended by `gnat stop`.
	Stopped bool
}

func newStatus(db *sql.DB, agent string) (*Status, error) {
	if _, err := db.Exec("INSERT OR IGNORE INTO statuses (agent) VALUES (?)", agent); err != nil {
		return nil, err
	}
	s := &Status{db: db, agent: agent}
	return s, s.load()
}

// load reads the agent's status from the database, leaving it as it is if the database
// has none.
func (s *Status) load() error {
	t, found, err := recorded.LoadTurn(s.db, s.agent)
	if err != nil || !found {
		return err
	}
	s.Busy, s.Turns, s.Exit, s.Reply, s.Error, s.Stopped = t.Busy, t.Turns, t.Exit, t.Reply, t.Error, t.Stopped
	return nil
}

func (s *Status) save() error {
	_, err := s.db.Exec("UPDATE statuses SET busy = ?, turns = ?, exit = ?, reply = ?, error = ?, stopped = ? WHERE agent = ?",
		b2i(s.Busy), s.Turns, s.Exit, s.Reply, s.Error, b2i(s.Stopped), s.agent)
	return err
}

// State is "starting", "busy", or "idle".
func (s *Status) State() string {
	switch {
	case !s.Started:
		return "starting"
	case s.Busy:
		return "busy"
	}
	return "idle"
}

// A usageTotal is what an agent's harness has used in total, from its usage events,
// kept in the database. A field no event has reported stays nil.
type usageTotal struct {
	db    *sql.DB
	agent string
	total events.UsageTotals
}

func newUsage(db *sql.DB, agent string) (*usageTotal, error) {
	if _, err := db.Exec("INSERT OR IGNORE INTO usage (agent) VALUES (?)", agent); err != nil {
		return nil, err
	}
	u := &usageTotal{db: db, agent: agent}
	return u, u.load()
}

// load reads the agent's totals from the database, leaving them as they are if the
// database has none.
func (u *usageTotal) load() error { return recorded.LoadUsage(u.db, u.agent, &u.total) }

// add totals a usage event's reported fields.
func (u *usageTotal) add(event events.Event) error {
	e := events.ParseUsage(event)
	t := &u.total
	for _, f := range [][2]**int64{{&t.InputTokens, &e.InputTokens}, {&t.CacheReadTokens, &e.CacheReadTokens},
		{&t.CacheWriteTokens, &e.CacheWriteTokens}, {&t.OutputTokens, &e.OutputTokens}} {
		if *f[1] != nil {
			sum := **f[1]
			if *f[0] != nil {
				sum += **f[0]
			}
			*f[0] = &sum
		}
	}
	if e.Cost != nil {
		sum := *e.Cost
		if t.Cost != nil {
			sum += *t.Cost
		}
		t.Cost = &sum
	}
	_, err := u.db.Exec("UPDATE usage SET input_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?, output_tokens = ?, cost = ? WHERE agent = ?",
		t.InputTokens, t.CacheReadTokens, t.CacheWriteTokens, t.OutputTokens, t.Cost, u.agent)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
