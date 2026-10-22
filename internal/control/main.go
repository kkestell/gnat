package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
	"github.com/kkestell/gnat/internal/web"
	// The control image has no CA certificates; the web UI's pull requests come
	// from GitHub over TLS.
	_ "golang.org/x/crypto/x509roots/fallback"
)

// call sends one request to the control plane in this container, over the socket only
// the container's own users can reach; tests replace it.
var call = func(req protocol.Request) (*protocol.Response, error) {
	return protocol.CallAddr(layout.ControlSocket, req, 0)
}

// An AgentResult is an agent's status from a wait.
type AgentResult struct {
	Agent string
	*protocol.Response
}

// WaitFor waits for every agent to be idle at once, within one overall timeout in
// seconds (nil for none), and returns each agent's status.
//
// Agents wake each other with messages, so one pass that finds each idle in turn isn't
// enough: pass again until one finds every agent idle with no turn finished since the
// pass before.
func WaitFor(agents []string, timeout *float64) ([]AgentResult, error) {
	var deadline time.Time
	if timeout != nil {
		deadline = time.Now().Add(time.Duration(*timeout * float64(time.Second)))
	}
	var previous []int
	for {
		results := make([]AgentResult, 0, len(agents))
		for _, agent := range agents {
			req := protocol.Request{Op: protocol.OpWait, Agent: agent}
			if timeout != nil {
				remaining := max(time.Until(deadline).Seconds(), 0)
				req.Timeout = &remaining
			}
			resp, err := call(req)
			if err != nil {
				return nil, err
			}
			results = append(results, AgentResult{agent, resp})
		}
		turns := make([]int, len(results))
		for i, r := range results {
			if r.Idle == nil || !*r.Idle {
				return results, nil
			}
			turns[i] = r.Turns
		}
		if equalInts(turns, previous) {
			return results, nil
		}
		previous = turns
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Report is each agent's final reply, headed by its name when there are several, and a
// line for each agent whose last turn failed.
func Report(results []AgentResult) (string, []string) {
	var out strings.Builder
	var failures []string
	for _, r := range results {
		if len(results) > 1 {
			fmt.Fprintf(&out, "==> %s <==\n", r.Agent)
		}
		if r.Reply != nil && *r.Reply != "" {
			out.WriteString(trimNewlines(*r.Reply) + "\n")
		}
		switch {
		case r.Stopped:
			failures = append(failures, fmt.Sprintf("%s: its last turn was stopped", r.Agent))
		case r.Turns != 0 && r.Exit == nil:
			failures = append(failures, fmt.Sprintf("%s: its last turn was interrupted by a restart or `gnat down`", r.Agent))
		case r.Exit != nil && *r.Exit != 0:
			reason := ""
			if r.Error != nil && *r.Error != "" {
				reason = ": " + *r.Error
			}
			failures = append(failures, fmt.Sprintf("%s: its last turn failed with status %d%s", r.Agent, *r.Exit, reason))
		}
	}
	return out.String(), failures
}

// A usageError is a bad command line, which exits with status 2.
type usageError string

func (e usageError) Error() string { return string(e) }

// parsed is a subcommand's arguments: flags may come before, between, and after the
// positionals, as with argparse.
type parsed struct {
	positional []string
	bools      map[string]bool
	values     map[string]string
}

func parseArgs(args []string, bools, values []string) (*parsed, error) {
	p := &parsed{bools: map[string]bool{}, values: map[string]string{}}
	has := func(set []string, name string) bool {
		for _, s := range set {
			if s == name {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			p.positional = append(p.positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			p.positional = append(p.positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		switch {
		case has(bools, name) && !hasValue:
			p.bools[name] = true
		case has(values, name):
			if !hasValue {
				if i+1 >= len(args) {
					return nil, usageError(fmt.Sprintf("argument --%s: expected one argument", name))
				}
				i++
				value = args[i]
			}
			p.values[name] = value
		default:
			return nil, usageError(fmt.Sprintf("unrecognized arguments: %s", arg))
		}
	}
	return p, nil
}

func (p *parsed) timeout() (*float64, error) {
	v, ok := p.values["timeout"]
	if !ok {
		return nil, nil
	}
	t, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, usageError(fmt.Sprintf("argument --timeout: invalid float value: '%s'", v))
	}
	return &t, nil
}

// Main runs `gnat-container control`: serve, the control plane itself, or one of the
// commands the host runs in its container (send, stop, wait, status). It returns the
// exit status.
func Main(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	err := dispatchCommand(args, stdout, stderr)
	switch e := err.(type) {
	case nil:
		return 0
	case exitError:
		return int(e)
	case usageError:
		fmt.Fprintf(stderr, "gnat-container control: error: %s\n", e)
		return 2
	}
	fmt.Fprintf(stderr, "error: %s\n", err)
	return 1
}

// An exitError ends the command with that status, its message already printed.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func dispatchCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("a command is required: serve, send, stop, wait, or status")
	}
	command, args := args[0], args[1:]
	switch command {
	case "serve":
		p, err := parseArgs(args, nil, []string{"setups", "repo", "swarm"})
		if err != nil {
			return err
		}
		if len(p.positional) == 0 {
			return usageError("serve needs the names of the swarm's agents")
		}
		setups := map[string]state.Agent{}
		if v, ok := p.values["setups"]; ok {
			if err := json.Unmarshal([]byte(v), &setups); err != nil {
				return usageError(fmt.Sprintf("argument --setups: invalid JSON value: %v", err))
			}
		}
		return serve(p.positional, setups, p.values["swarm"], p.values["repo"])
	case "send":
		p, err := parseArgs(args, []string{"now", "new", "wait"}, []string{"timeout"})
		if err != nil {
			return err
		}
		if len(p.positional) < 2 {
			return usageError("send needs a message and at least one agent")
		}
		timeout, err := p.timeout()
		if err != nil {
			return err
		}
		text, agents := p.positional[0], p.positional[1:]
		for _, agent := range agents {
			if _, err := call(protocol.Request{Op: protocol.OpSend, Agent: agent, Text: text,
				Now: p.bools["now"], New: p.bools["new"]}); err != nil {
				return err
			}
		}
		if !p.bools["wait"] {
			return nil
		}
		return waitAndReport(agents, timeout, stdout, stderr)
	case "stop":
		p, err := parseArgs(args, nil, nil)
		if err != nil {
			return err
		}
		if len(p.positional) == 0 {
			return usageError("stop needs at least one agent")
		}
		for _, agent := range p.positional {
			resp, err := call(protocol.Request{Op: protocol.OpStop, Agent: agent})
			if err != nil {
				return err
			}
			state := "not running a turn"
			if resp.Stopping != nil && *resp.Stopping {
				state = "stopping its turn"
			}
			fmt.Fprintf(stdout, "%s: %s\n", agent, state)
		}
		return nil
	case "wait":
		p, err := parseArgs(args, nil, []string{"timeout"})
		if err != nil {
			return err
		}
		if len(p.positional) == 0 {
			return usageError("wait needs at least one agent")
		}
		timeout, err := p.timeout()
		if err != nil {
			return err
		}
		return waitAndReport(p.positional, timeout, stdout, stderr)
	case "status":
		p, err := parseArgs(args, nil, nil)
		if err != nil {
			return err
		}
		if len(p.positional) == 0 {
			return usageError("status needs at least one agent")
		}
		for _, agent := range p.positional {
			resp, err := call(protocol.Request{Op: protocol.OpStatus, Agent: agent})
			if err != nil {
				return err
			}
			line, err := protocol.Encode(protocol.StatusLine(agent, resp))
			if err != nil {
				return err
			}
			stdout.Write(line)
		}
		return nil
	}
	return usageError(fmt.Sprintf("unknown command '%s': use serve, send, stop, wait, or status", command))
}

func waitAndReport(agents []string, timeout *float64, stdout, stderr io.Writer) error {
	results, err := WaitFor(agents, timeout)
	if err != nil {
		return err
	}
	var busy []string
	for _, r := range results {
		if r.Idle == nil || !*r.Idle {
			busy = append(busy, r.Agent)
		}
	}
	if len(busy) > 0 {
		fmt.Fprintf(stderr, "error: timed out waiting for %s\n", strings.Join(busy, ", "))
		return exitError(layout.TimeoutStatus)
	}
	out, failures := Report(results)
	io.WriteString(stdout, out)
	for _, failure := range failures {
		fmt.Fprintln(stderr, failure)
	}
	if len(failures) > 0 {
		return exitError(1)
	}
	return nil
}

// listenOperator listens on the socket at path for the operator's commands, which only the
// user the control plane runs as can connect to.
func listenOperator(path string) (net.Listener, error) {
	_ = os.Remove(path) // The last run's.
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// serve runs the control plane, the web UI, and git for the agents until the container
// is stopped. With GH_TOKEN, the UI shows the repository's pull requests, and the agents
// whose secrets are in GNAT_SECRETS can act on GitHub, and clone, fetch, and push, through
// the control plane.
func serve(agents []string, setups map[string]state.Agent, swarmName, repo string) error {
	token := os.Getenv("GH_TOKEN")
	swarm := web.Swarm{Repo: repo, Started: time.Now(), Token: token}
	secrets := map[string]string{}
	if v := os.Getenv(state.SecretsVar); v != "" {
		if err := json.Unmarshal([]byte(v), &secrets); err != nil {
			return fmt.Errorf("%s isn't a JSON object of agent secrets: %w", state.SecretsVar, err)
		}
	}
	db, err := Prepare(layout.LogDir, agents)
	if err != nil {
		return err
	}
	c, err := New(agents, db, setups, layout.LogDir)
	if err != nil {
		return err
	}
	if err := c.SetSecrets(secrets); err != nil {
		return err
	}
	if repo != "" && swarmName != "" {
		if err := c.EnableGitHub(GitHub{Swarm: swarmName, Repo: repo, Token: token}); err != nil {
			return err
		}
	}
	var proxy *GitProxy
	if repo != "" && swarmName != "" {
		if proxy, err = c.NewGitProxy(GitConfig{Swarm: swarmName, Repo: repo, Token: token}); err != nil {
			return err
		}
	}
	// Agents come in over the network, and every request they make must have their
	// secret. The operator's commands come in by `docker exec`, over a socket that only
	// the container's own user can use, and need none.
	agentsLn, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", protocol.Port))
	if err != nil {
		return err
	}
	operatorLn, err := listenOperator(layout.ControlSocket)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for _, run := range []func() error{
		func() error { return c.ServeAgents(ctx, agentsLn) },
		func() error { return c.Serve(ctx, operatorLn) },
		func() error { return web.Serve(ctx, fmt.Sprintf("0.0.0.0:%d", layout.WebPort), c, swarm) },
		func() error {
			if proxy == nil {
				return nil
			}
			// Fetch now, so the agents' first clones needn't wait for it.
			go func() {
				if err := proxy.Sync(ctx); err != nil {
					log.Printf("git: fetching %s: %v", repo, err)
				}
			}()
			return ServeGit(ctx, fmt.Sprintf("0.0.0.0:%d", layout.GitPort), proxy)
		},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(); err != nil {
				errs <- err
				stop()
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}
