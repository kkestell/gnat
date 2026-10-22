// Package control is the control plane: it queues messages for each agent and logs what
// each agent does with them.
//
// Each agent daemon keeps one connection to it, opened with a protocol.Request whose Op
// is "attach", on which both ends then send protocol.Lines. The control plane pushes a
// "turn" (Text, New) to start a turn and a "stop" to end the running one; the daemon
// sends an "event" (Event; see the events package) for each of the turn's events and a
// "done" (Exit, Stopped) when it ends. A line the control plane can't use gets an
// "error" back. Every other connection is one request (see the protocol package) and
// its response.
package control

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/recorded"
	"github.com/kkestell/gnat/internal/render"
	"github.com/kkestell/gnat/internal/state"
)

// EventsFile records what every agent sees and does, one {"agent", "event"} object per
// line, for the web UI. Events are the agents' (see the events package) and the control
// plane's own: {"type": "swarm_message", "text", "sender", "new", "mid_turn"} for each
// message an agent receives (the sender is an agent's name, null for the operator; new
// is true when it started a fresh session), and {"type": "swarm_done", "exit",
// "stopped"} when its turn ends, with a null exit if the agent restarted before the turn
// could finish, and stopped true if `gnat stop` ended it. A message one agent sends
// another is also the sender's {"type": "swarm_sent", "to", "text"}.
const EventsFile = "events.jsonl"

// DefaultMaxRequestBytes is the longest line the control plane reads. One event can be
// far longer than a typical line limit: a long Claude thinking block's signature alone
// reaches 64 KiB.
const DefaultMaxRequestBytes = 64 * 1024 * 1024

// ErrCancelled is what a cancelled wait for a message returns.
var ErrCancelled = errors.New("cancelled")

// A broadcast wakes everything waiting on it: wait returns a channel that closes at the
// next notify.
type broadcast struct {
	mu sync.Mutex
	ch chan struct{}
}

func (b *broadcast) wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	return b.ch
}

func (b *broadcast) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		close(b.ch)
		b.ch = nil
	}
}

// A Control is the control plane's state: a mailbox, status, and usage per agent.
type Control struct {
	// MaxRequestBytes is the longest line it reads.
	MaxRequestBytes int

	agents []string
	// setups is each agent's harness, model, and effort, by name, for the web UI.
	setups map[string]state.Agent
	logDir string
	// github is the agents' access to GitHub; nil if the control plane has none.
	github *GitHub
	// secrets are the agents' secrets, hashed.
	secrets map[string][sha256.Size]byte

	mailboxes map[string]*Mailbox

	// mu guards the statuses, usage, active, and daemons.
	mu       sync.Mutex
	statuses map[string]*Status
	usage    map[string]*usageTotal
	// active is when each agent last started a turn or sent an event.
	active map[string]time.Time
	// daemons are each agent's attached daemon.
	daemons map[string]*Daemon
	// turnEnded is notified whenever a turn ends, for wait.
	turnEnded broadcast

	// recMu guards events.jsonl and the watchers.
	recMu    sync.Mutex
	watchers map[*watcher]bool
}

// New is the control plane for the agents, keeping its state in db and its logs in
// logDir, which it must find in place (see Prepare). setups have the agents' IDs; an agent
// without one has none.
func New(agents []string, db *sql.DB, setups map[string]state.Agent, logDir string) (*Control, error) {
	if setups == nil {
		setups = map[string]state.Agent{}
	}
	c := &Control{
		MaxRequestBytes: DefaultMaxRequestBytes,
		agents:          agents, setups: setups, logDir: logDir,
		mailboxes: map[string]*Mailbox{}, statuses: map[string]*Status{}, usage: map[string]*usageTotal{},
		active: map[string]time.Time{}, daemons: map[string]*Daemon{}, watchers: map[*watcher]bool{},
	}
	// Forget agents the swarm no longer has, so one added later by the same name starts fresh.
	args := make([]any, len(agents))
	marks := make([]string, len(agents))
	for i, agent := range agents {
		args[i], marks[i] = agent, "?"
	}
	for _, table := range []string{"messages", "statuses", "usage", "agent_ids"} {
		query := fmt.Sprintf("DELETE FROM %s WHERE agent NOT IN (%s)", table, strings.Join(marks, ", "))
		if _, err := db.Exec(query, args...); err != nil {
			return nil, err
		}
	}
	// And forget everything about an agent that was deleted and created again while the
	// swarm was down: the new one has another ID (see recorded.Current).
	forgotten := map[string]bool{}
	for _, agent := range agents {
		current, err := recorded.Current(db, agent, setups[agent].ID)
		if err != nil {
			return nil, err
		}
		if !current {
			forgotten[agent] = true
		}
	}
	// Records first: the ID, recorded last, marks the forgetting done.
	if err := c.dropRecords(forgotten); err != nil {
		return nil, err
	}
	for agent := range forgotten {
		if err := c.forget(db, agent, setups[agent].ID); err != nil {
			return nil, err
		}
	}
	for _, agent := range agents {
		c.mailboxes[agent] = NewMailbox(db, agent)
		var err error
		if c.statuses[agent], err = newStatus(db, agent); err != nil {
			return nil, err
		}
		if c.usage[agent], err = newUsage(db, agent); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// forget clears the agent's log, queue, status, and usage, which were another agent's by
// the same name, and records its ID as the one they now belong to.
func (c *Control) forget(db *sql.DB, agent, id string) error {
	if err := os.Truncate(c.logPath(agent), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"messages", "statuses", "usage"} {
		if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE agent = ?", table), agent); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO agent_ids (agent, id) VALUES (?, ?)", agent, id); err != nil {
		return err
	}
	return tx.Commit()
}

// dropRecords rewrites events.jsonl without the agents' records, if it has any.
func (c *Control) dropRecords(agents map[string]bool) error {
	if len(agents) == 0 {
		return nil
	}
	path := filepath.Join(c.logDir, EventsFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var kept bytes.Buffer
	dropped := false
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		var head struct {
			Agent string `json:"agent"`
		}
		if json.Unmarshal(line, &head) == nil && agents[head.Agent] {
			dropped = true
			continue
		}
		kept.Write(line)
	}
	if !dropped {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, kept.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Prepare creates the log directory, each agent's log, and events.jsonl if they aren't
// there, and opens the database.
func Prepare(logDir string, agents []string) (*sql.DB, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	for _, name := range append([]string{EventsFile}, logNames(agents)...) {
		f, err := os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	return Connect(filepath.Join(logDir, recorded.DBFile))
}

func logNames(agents []string) []string {
	names := make([]string, len(agents))
	for i, agent := range agents {
		names[i] = agent + ".log"
	}
	return names
}

// Agents are the swarm's agents, in order.
func (c *Control) Agents() []string { return c.agents }

// Setups is each agent's harness, model, and effort, by name.
func (c *Control) Setups() map[string]state.Agent { return c.setups }

// Idle is whether the agent has finished every message sent to it.
func (c *Control) idle(agent string) bool {
	// Look at the queue first: a message leaves it only as its agent turns busy (see
	// pushTurns), so a queue found empty means the busy flag that follows is current.
	pending := c.mailboxes[agent].Pending()
	c.mu.Lock()
	busy := c.statuses[agent].Busy
	c.mu.Unlock()
	return pending == 0 && !busy
}

// describe is the agent's status as a successful response.
func (c *Control) describe(agent string) *protocol.Response {
	queued := c.mailboxes[agent].Pending()
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.statuses[agent]
	r := protocol.OK()
	r.State, r.Queued, r.Turns, r.Exit, r.Stopped = s.State(), queued, s.Turns, s.Exit, s.Stopped
	r.Reply, r.Error = s.Reply, s.Error
	total := c.usage[agent].total
	r.Usage = &total
	if t, ok := c.active[agent]; ok {
		since := time.Since(t).Seconds()
		r.SinceActive = &since
	}
	return r
}

func (c *Control) logPath(agent string) string { return filepath.Join(c.logDir, agent+".log") }

func (c *Control) log(agent, text string) {
	// Open per write so `tail -f` sees each line as soon as it arrives.
	f, err := os.OpenFile(c.logPath(agent), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = f.WriteString(text)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("control: writing %s's log: %v", agent, err)
	}
}

// event logs one of the agent's events and records it for the web UI.
func (c *Control) event(agent string, event events.Event) {
	c.mu.Lock()
	c.active[agent] = time.Now()
	c.mu.Unlock()
	if text := render.Render(event); text != "" {
		c.log(agent, text)
	}
	c.mu.Lock()
	switch event.Type() {
	case events.Usage:
		logErr(c.usage[agent].add(event))
	case events.TurnEnd:
		s := c.statuses[agent]
		s.Reply, s.Error = stringOrNil(event["reply"]), stringOrNil(event["error"])
		logErr(s.save())
	}
	c.mu.Unlock()
	c.record(agent, event)
}

func stringOrNil(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func logErr(err error) {
	if err != nil {
		log.Printf("control: %v", err)
	}
}

// record appends an event to events.jsonl and hands it to every watcher.
func (c *Control) record(agent string, event events.Event) {
	line, err := protocol.Encode(struct {
		Agent string       `json:"agent"`
		Event events.Event `json:"event"`
	}{agent, event})
	if err != nil {
		log.Printf("control: recording %s's event: %v", agent, err)
		return
	}
	c.recMu.Lock()
	defer c.recMu.Unlock()
	f, err := os.OpenFile(filepath.Join(c.logDir, EventsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = f.Write(line)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("control: recording %s's event: %v", agent, err)
	}
	record := strings.TrimSuffix(string(line), "\n")
	for w := range c.watchers {
		w.push(record)
	}
}

// A watcher is a queue of records, unbounded so recording never waits on a slow browser.
type watcher struct {
	mu    sync.Mutex
	queue []string
	wake  chan struct{}
	done  chan struct{}
	out   chan string
}

func (w *watcher) push(record string) {
	w.mu.Lock()
	w.queue = append(w.queue, record)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// run feeds the queue to out until done closes.
func (w *watcher) run() {
	for {
		w.mu.Lock()
		queue := w.queue
		w.queue = nil
		w.mu.Unlock()
		for _, record := range queue {
			select {
			case w.out <- record:
			case <-w.done:
				return
			}
		}
		select {
		case <-w.wake:
		case <-w.done:
			return
		}
	}
}

// Watch is a channel of every event record so far, then each new one as it's recorded,
// and a function that stops watching.
func (c *Control) Watch() (<-chan string, func()) {
	w := &watcher{wake: make(chan struct{}, 1), done: make(chan struct{}), out: make(chan string)}
	c.recMu.Lock()
	if f, err := os.Open(filepath.Join(c.logDir, EventsFile)); err == nil {
		r := bufio.NewReader(f)
		for {
			line, err := r.ReadString('\n')
			if record := strings.TrimSuffix(line, "\n"); record != "" {
				var head struct {
					Agent string `json:"agent"`
				}
				// Earlier runs may have had agents this one doesn't.
				if json.Unmarshal([]byte(record), &head) == nil && c.mailboxes[head.Agent] != nil {
					w.queue = append(w.queue, record)
				}
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	c.watchers[w] = true
	c.recMu.Unlock()
	go w.run()
	var once sync.Once
	return w.out, func() {
		once.Do(func() {
			c.recMu.Lock()
			delete(c.watchers, w)
			c.recMu.Unlock()
			close(w.done)
		})
	}
}

// A tooLongError is a line over the limit, which readLine discards.
type tooLongError struct{ max int }

func (e tooLongError) Error() string { return fmt.Sprintf("line longer than %d bytes", e.max) }

// readLine reads one line of at most max bytes, without its newline. A longer line is
// discarded through its end and reported as a tooLongError. A last line without a
// newline comes with io.EOF.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	over := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !over {
			line = append(line, chunk...)
			if len(line) > max+1 {
				over, line = true, nil
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if over && (err == nil || err == io.EOF) {
			return nil, tooLongError{max}
		}
		if err != nil && (err != io.EOF || len(line) == 0) {
			return nil, err
		}
		return bytes.TrimSuffix(line, []byte("\n")), err
	}
}
