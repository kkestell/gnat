package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/kkestell/gnat/internal/events"
	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/recorded"
	"github.com/kkestell/gnat/internal/render"
	"github.com/kkestell/gnat/internal/state"
)

// action is a command's work, which fails with an error that gnat reports and exits 1
// (or with the status of a command it ran) on, or with a usageError, which exits 2.
type action func(cmd *cobra.Command, args []string) error

// run wraps an action so that its errors are told apart from cobra's own.
func (a action) run(cmd *cobra.Command, args []string) error {
	err := a(cmd, args)
	if _, usage := err.(*usageError); err == nil || usage {
		return err
	}
	return &failure{err}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "gnat",
		Short:         "Run a swarm of coding agents, each in its own Docker container",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q", args[0])
			}
			return nil
		},
		RunE: needSubcommand,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	// Replace the `help` command, which the Python CLI doesn't have, with one nothing can
	// run or see; -h still works.
	root.SetHelpCommand(&cobra.Command{Use: "no-help", Hidden: true})
	root.AddCommand(
		createCommand(), deleteCommand(), listCommand(), upCommand(), downCommand(),
		sendCommand(), stopCommand(), waitCommand(), statusCommand(), copyCommand(),
		logsCommand(), webCommand(), agentCommands(), fileCommands(), profileCommands(), authCommands(),
	)
	return root
}

// needSubcommand is the run of a command that only groups others.
func needSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return &usageError{fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())}
	}
	return &usageError{fmt.Sprintf("a command is required for %q", cmd.CommandPath())}
}

func group(use, short string, subcommands ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, RunE: needSubcommand, SilenceUsage: true, SilenceErrors: true}
	c.AddCommand(subcommands...)
	return c
}

func timeoutFlag(c *cobra.Command, timeout *float64) {
	c.Flags().Float64Var(timeout, "timeout", 0, fmt.Sprintf(
		"give up waiting after `SECONDS`, with exit status %d", layout.TimeoutStatus))
}

const agentsUsage = " [AGENT...]"

func createCommand() *cobra.Command {
	var dockerfile, repo, profile string
	var files []string
	c := &cobra.Command{
		Use: "create SWARM", Short: "define a new swarm", Args: cobra.ExactArgs(1),
		RunE: action(func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("profile") {
				return createFromProfile(args[0], dockerfile, repo, files, profile)
			}
			return create(args[0], dockerfile, repo, files)
		}).run,
	}
	c.Flags().StringVar(&profile, "profile", "", "add the agents of the swarm `PROFILE`: a name in gnat's config profiles/swarms directory, or a YAML file's path")
	c.Flags().StringVar(&dockerfile, "dockerfile", "", "Dockerfile extending gnat-agent for the agent image (default: gnat-agent)")
	c.Flags().StringVar(&repo, "repo", "", "GitHub repository each agent clones, `OWNER/NAME`")
	c.Flags().StringArrayVar(&files, "file", nil, "copy a file outside source control into each agent's /workspace "+
		"(default path: the file's name); repeatable, `HOST_PATH[:WORKSPACE_PATH]`")
	_ = c.MarkFlagRequired("repo")
	return c
}

func deleteCommand() *cobra.Command {
	return &cobra.Command{
		Use: "delete SWARM", Args: cobra.ExactArgs(1),
		Short: "bring a swarm down and delete it, with its logs and agents' clones and sessions",
		RunE:  action(func(_ *cobra.Command, args []string) error { return deleteSwarm(args[0]) }).run,
	}
}

func listCommand() *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "list swarms", Args: cobra.NoArgs,
		RunE: action(func(*cobra.Command, []string) error { return listSwarms() }).run,
	}
}

func upCommand() *cobra.Command {
	return &cobra.Command{
		Use: "up SWARM", Short: "start a swarm's control plane and agents", Args: cobra.ExactArgs(1),
		RunE: action(func(_ *cobra.Command, args []string) error { return up(args[0]) }).run,
	}
}

func downCommand() *cobra.Command {
	return &cobra.Command{
		Use: "down SWARM", Args: cobra.ExactArgs(1),
		Short: "stop and remove a swarm's containers, keeping agents' clones, sessions, and queues",
		RunE:  action(func(_ *cobra.Command, args []string) error { return down(args[0]) }).run,
	}
}

func sendCommand() *cobra.Command {
	var message string
	var now, fresh, wait bool
	var timeout float64
	c := &cobra.Command{Use: "send SWARM" + agentsUsage, Short: "queue a message for agents", Args: cobra.MinimumNArgs(1)}
	c.RunE = action(func(cmd *cobra.Command, args []string) error {
		o := sendOptions{now: now, fresh: fresh, wait: wait}
		if cmd.Flags().Changed("message") {
			o.message = &message
		}
		if cmd.Flags().Changed("timeout") {
			o.timeout = &timeout
		}
		return send(args[0], args[1:], o)
	}).run
	c.Flags().StringVarP(&message, "message", "m", "", "message `TEXT` (default: read stdin)")
	c.Flags().BoolVar(&now, "now", false, "deliver at each agent's next tool call instead of after its turn")
	c.Flags().BoolVar(&fresh, "new", false, "start the message in a fresh session; later messages continue it")
	c.Flags().BoolVar(&wait, "wait", false, "then wait for the agents to finish and print their replies, as `wait` does")
	timeoutFlag(c, &timeout)
	return c
}

func stopCommand() *cobra.Command {
	return &cobra.Command{
		Use: "stop SWARM" + agentsUsage, Args: cobra.MinimumNArgs(1),
		Short: "end agents' running turns, keeping their queues and sessions",
		RunE:  action(func(_ *cobra.Command, args []string) error { return stop(args[0], args[1:]) }).run,
	}
}

func waitCommand() *cobra.Command {
	var timeout float64
	var anyOf bool
	c := &cobra.Command{
		Use: "wait SWARM" + agentsUsage, Args: cobra.MinimumNArgs(1),
		Short: "wait for agents to finish every message sent to them and print their replies",
	}
	c.RunE = action(func(cmd *cobra.Command, args []string) error {
		var t *float64
		if cmd.Flags().Changed("timeout") {
			t = &timeout
		}
		return wait(args[0], args[1:], t, anyOf)
	}).run
	c.Flags().BoolVar(&anyOf, "any", false, "return once at least one of the agents has finished, "+
		"printing the replies of those that have, each under its name, and the ones still busy; one already finished counts at once")
	timeoutFlag(c, &timeout)
	return c
}

func statusCommand() *cobra.Command {
	return &cobra.Command{
		Use: "status SWARM" + agentsUsage, Short: "show what each agent is doing", Args: cobra.MinimumNArgs(1),
		RunE: action(func(_ *cobra.Command, args []string) error { return status(args[0], args[1:]) }).run,
	}
}

func copyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cp SWARM AGENT:PATH HOST_PATH",
		Short: "copy a file or directory out of an agent's container or clone",
		Long: "Copy a file or directory out of an agent's container or clone.\n\n" +
			"A relative PATH is in the agent's clone, /workspace. A HOST_PATH of - writes a tar to stdout.\n\n" +
			"HOST_PATH is treated as docker cp treats it. A directory copied to an existing directory goes inside it, " +
			"and to a path that doesn't exist creates that path with the directory's contents. " +
			"A PATH ending in /. copies the directory's contents instead, so AGENT:. copies the clone's contents. " +
			"A file copied to an existing directory goes inside it, and to any other path is written to that path.",
		Args: cobra.ExactArgs(3),
		RunE: action(func(_ *cobra.Command, args []string) error { return copyOut(args[0], args[1], args[2]) }).run,
	}
}

func logsCommand() *cobra.Command {
	var lines int
	var follow bool
	c := &cobra.Command{Use: "logs SWARM" + agentsUsage, Short: "show agents' messages and output", Args: cobra.MinimumNArgs(1)}
	c.RunE = action(func(cmd *cobra.Command, args []string) error {
		tail := "+1"
		if cmd.Flags().Changed("lines") {
			tail = strconv.Itoa(lines)
		}
		return logs(args[0], args[1:], tail, follow)
	}).run
	c.Flags().IntVarP(&lines, "lines", "n", 0, "show only the last `LINES` lines of each log")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output")
	return c
}

func webCommand() *cobra.Command {
	var noOpen bool
	c := &cobra.Command{
		Use: "web SWARM", Short: "print a swarm's web UI URL and open it in the browser", Args: cobra.ExactArgs(1),
		RunE: action(func(_ *cobra.Command, args []string) error { return showWeb(args[0], !noOpen) }).run,
	}
	c.Flags().BoolVar(&noOpen, "no-open", false, "print the URL only")
	return c
}

func agentCommands() *cobra.Command {
	var profileName string
	var options agentOptions
	create := &cobra.Command{Use: "create SWARM AGENT", Short: "add an agent to a swarm", Args: cobra.ExactArgs(2)}
	create.RunE = action(func(cmd *cobra.Command, args []string) error {
		resolved := agentOptions{Harness: state.DefaultHarness}
		if cmd.Flags().Changed("profile") {
			var err error
			resolved, err = loadProfile(profileName, "")
			if err != nil {
				return err
			}
		}
		for _, setting := range []struct {
			flag   string
			value  string
			target *string
		}{
			{"instructions", options.Instructions, &resolved.Instructions},
			{"harness", options.Harness, &resolved.Harness},
			{"model", options.Model, &resolved.Model},
			{"effort", options.Effort, &resolved.Effort},
		} {
			if cmd.Flags().Changed(setting.flag) {
				*setting.target = setting.value
			}
		}
		if !slices.Contains(harness.Names(), resolved.Harness) {
			return usagef("invalid --harness %q (choose from %s)", resolved.Harness, strings.Join(harness.Names(), ", "))
		}
		if cmd.Flags().Changed("merge") {
			resolved.Merge = options.Merge
		}
		return createAgent(args[0], args[1], resolved.Instructions, resolved.Harness, resolved.Model, resolved.Effort, resolved.Merge)
	}).run
	create.Flags().StringVar(&profileName, "profile", "", "load agent settings from `PROFILE`: a name in gnat's config profiles/agents directory, or a YAML file's path")
	create.Flags().StringVar(&options.Instructions, "instructions", "", "additional instructions for the agent")
	create.Flags().StringVar(&options.Harness, "harness", state.DefaultHarness,
		"the program the agent runs its turns with ("+strings.Join(harness.Names(), ", ")+")")
	create.Flags().StringVar(&options.Model, "model", "",
		"model (default: the harness's default); for ox, a qualified ID such as openrouter:deepseek/deepseek-v4.1-flash")
	create.Flags().BoolVar(&options.Merge, "merge", false, "let the agent merge pull requests")
	var efforts []string
	for _, h := range harness.All() {
		efforts = append(efforts, fmt.Sprintf("for %s, %s", h.Name(), strings.Join(h.Efforts(), ", ")))
	}
	create.Flags().StringVar(&options.Effort, "effort", "",
		"effort level (default: the harness's default): "+strings.Join(efforts, "; "))

	del := &cobra.Command{
		Use: "delete SWARM AGENT", Args: cobra.ExactArgs(2),
		Short: "remove an agent from a swarm, with its clone and sessions",
		RunE:  action(func(_ *cobra.Command, args []string) error { return deleteAgent(args[0], args[1]) }).run,
	}
	list := &cobra.Command{
		Use: "list SWARM", Short: "list a swarm's agents", Args: cobra.ExactArgs(1),
		RunE: action(func(_ *cobra.Command, args []string) error { return listAgents(args[0]) }).run,
	}
	return group("agent", "manage a swarm's agents", create, del, list)
}

func fileCommands() *cobra.Command {
	add := &cobra.Command{
		Use: "add SWARM HOST_PATH[:WORKSPACE_PATH]", Short: "add a file to a swarm", Args: cobra.ExactArgs(2),
		RunE: action(func(_ *cobra.Command, args []string) error { return addSwarmFile(args[0], args[1]) }).run,
	}
	del := &cobra.Command{
		Use: "delete SWARM WORKSPACE_PATH", Short: "remove a file from a swarm", Args: cobra.ExactArgs(2),
		RunE: action(func(_ *cobra.Command, args []string) error { return deleteSwarmFile(args[0], args[1]) }).run,
	}
	list := &cobra.Command{
		Use: "list SWARM", Short: "list a swarm's files", Args: cobra.ExactArgs(1),
		RunE: action(func(_ *cobra.Command, args []string) error { return listFiles(args[0]) }).run,
	}
	return group("file", "manage a swarm's files outside source control", add, del, list)
}

func create(name, dockerfile, repo string, fileSpecs []string) error {
	swarm, err := newSwarm(name, dockerfile, repo, fileSpecs)
	if err != nil {
		return err
	}
	if err := state.Save(swarm); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created swarm %s\n", name)
	return nil
}

// createFromProfile creates a swarm with a swarm profile's agents, or nothing if any is invalid.
func createFromProfile(name, dockerfile, repo string, fileSpecs []string, profile string) error {
	swarm, err := newSwarm(name, dockerfile, repo, fileSpecs)
	if err != nil {
		return err
	}
	agents, err := loadSwarmProfile(profile)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a, err = checkAgent(swarm, a); err != nil {
			return fmt.Errorf("swarm profile %q: %w", profile, err)
		}
		if a.ID, err = newAgentID(); err != nil {
			return err
		}
		swarm.Agents = append(swarm.Agents, a)
	}
	if err := state.Save(swarm); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created swarm %s with agents %s\n", name, strings.Join(agentNames(swarm.Agents), ", "))
	return nil
}

// newSwarm checks a new swarm's settings and returns it, unsaved and without agents.
func newSwarm(name, dockerfile, repo string, fileSpecs []string) (*state.Swarm, error) {
	if err := state.CheckName(name, "swarm name"); err != nil {
		return nil, err
	}
	if exists, err := state.Exists(name); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("swarm '%s' already exists", name)
	}
	if dockerfile != "" {
		abs, err := filepath.Abs(dockerfile)
		if err != nil {
			return nil, err
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		if !isFile(abs) {
			return nil, fmt.Errorf("dockerfile not found: %s", abs)
		}
		dockerfile = abs
	}
	var files []state.File
	for _, spec := range fileSpecs {
		file, err := state.ParseFile(spec)
		if err != nil {
			return nil, err
		}
		if files, err = addFile(files, file); err != nil {
			return nil, err
		}
	}
	return &state.Swarm{Name: name, Dockerfile: dockerfile, Repo: repo, Files: files}, nil
}

func addFile(files []state.File, file state.File) ([]state.File, error) {
	for _, f := range files {
		if f.Path == file.Path {
			return nil, fmt.Errorf("two files go to %s in the workspace", file.Path)
		}
	}
	return append(files, file), nil
}

func deleteSwarm(name string) error {
	swarm, err := state.Load(name)
	if err != nil {
		return err
	}
	if _, err := teardown(name); err != nil {
		return err
	}
	names, err := volumes(name, "")
	if err != nil {
		return err
	}
	if len(names) > 0 {
		if err := dockerQuiet(append([]string{"volume", "rm"}, names...)...); err != nil {
			return err
		}
	}
	if swarm.Dockerfile != "" {
		// The image may be gone already.
		_, _ = execute(command{name: "docker", args: []string{"rmi", imageName(name)}, capture: true})
	}
	if err := state.Delete(name); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "deleted swarm %s\n", name)
	return nil
}

func listSwarms() error {
	names, err := state.Names()
	if err != nil {
		return err
	}
	for _, name := range names {
		swarm, err := state.Load(name)
		if err != nil {
			return err
		}
		running, err := containers(name)
		if err != nil {
			return err
		}
		status := "down"
		if len(running) > 0 {
			status = "up"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%d agents\t%s\n", name, status, len(swarm.Agents), swarm.Repo)
	}
	return nil
}

func down(name string) error {
	if _, err := state.Load(name); err != nil {
		return err
	}
	names, err := teardown(name)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintf(stdout, "swarm '%s' is not up\n", name)
	}
	for _, n := range names {
		fmt.Fprintf(stdout, "removed %s\n", n)
	}
	return nil
}

func createAgent(swarmName, agentName, instructions, harnessName, model, effort string, merge bool) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	a, err := checkAgent(swarm, state.Agent{
		Name: agentName, Instructions: instructions, Model: model, Effort: effort, Harness: harnessName, Merge: merge,
	})
	if err != nil {
		return err
	}
	if err := requireDown(swarm.Name); err != nil {
		return err
	}
	if a.ID, err = newAgentID(); err != nil {
		return err
	}
	// A log an earlier agent by its name left (deleted by a gnat before IDs) isn't its own.
	if err := removeLog(swarm.Name, a.Name); err != nil {
		return err
	}
	swarm.Agents = append(swarm.Agents, a)
	if err := state.Save(swarm); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created agent %s in swarm %s\n", agentName, swarm.Name)
	return nil
}

// checkAgent checks that a can join swarm, returning it with its harness's canonical name.
func checkAgent(swarm *state.Swarm, a state.Agent) (state.Agent, error) {
	if err := state.CheckName(a.Name, "agent name"); err != nil {
		return a, err
	}
	if a.Name == state.Control {
		return a, fmt.Errorf("'%s' is reserved for the control plane", state.Control)
	}
	for _, existing := range swarm.Agents {
		if existing.Name == a.Name {
			return a, fmt.Errorf("swarm '%s' already has an agent '%s'", swarm.Name, a.Name)
		}
	}
	// The agent's branches are named after the swarm and the agent, so both must make valid refs.
	if _, err := state.BranchPrefix(swarm.Name, a.Name); err != nil {
		return a, err
	}
	h, err := harness.Get(a.Harness)
	if err != nil {
		return a, err
	}
	if a.Effort != "" && !slices.Contains(h.Efforts(), a.Effort) {
		return a, fmt.Errorf("%s effort must be one of %s", h.Name(), strings.Join(h.Efforts(), ", "))
	}
	a.Harness = h.Name()
	return a, nil
}

func deleteAgent(swarmName, agentName string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	agent, err := swarm.Agent(agentName)
	if err != nil {
		return err
	}
	if err := requireDown(swarm.Name); err != nil {
		return err
	}
	swarm.Agents = slices.DeleteFunc(swarm.Agents, func(a state.Agent) bool { return a.Name == agent.Name })
	if err := state.Save(swarm); err != nil {
		return err
	}
	names, err := volumes(swarm.Name, agent.Name)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		if err := dockerQuiet(append([]string{"volume", "rm"}, names...)...); err != nil {
			return err
		}
	}
	// The control plane forgets the rest of its history at the next `up`, by its ID.
	if err := removeLog(swarm.Name, agent.Name); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "deleted agent %s in swarm %s\n", agent.Name, swarm.Name)
	return nil
}

// removeLog removes the agent's log from the swarm's logs on the host, if it's there.
func removeLog(swarm, agent string) error {
	logs, err := state.LogsDirectory(swarm)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(logs, agent+".log")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func listAgents(swarmName string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	for _, a := range swarm.Agents {
		merge := "-"
		if a.Merge {
			merge = "merge"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Harness, orDash(a.Model), orDash(a.Effort), merge)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func addSwarmFile(swarmName, spec string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	file, err := state.ParseFile(spec)
	if err != nil {
		return err
	}
	if !isFile(file.Host) {
		return fmt.Errorf("file not found: %s", file.Host)
	}
	if err := requireDown(swarm.Name); err != nil {
		return err
	}
	if swarm.Files, err = addFile(swarm.Files, file); err != nil {
		return err
	}
	if err := state.Save(swarm); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "added %s as %s in swarm %s\n", file.Host, file.Path, swarm.Name)
	return nil
}

func deleteSwarmFile(swarmName, workspacePath string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	path, err := state.CheckWorkspacePath(workspacePath)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(swarm.Files, func(f state.File) bool { return f.Path == path })
	if i < 0 {
		return fmt.Errorf("swarm '%s' has no file at %s", swarm.Name, path)
	}
	if err := requireDown(swarm.Name); err != nil {
		return err
	}
	swarm.Files = slices.Delete(swarm.Files, i, i+1)
	if err := state.Save(swarm); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "deleted file %s in swarm %s\n", path, swarm.Name)
	return nil
}

func listFiles(swarmName string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	for _, f := range swarm.Files {
		fmt.Fprintf(stdout, "%s\t%s\n", f.Path, f.Host)
	}
	return nil
}

// selectAgents are the named agents in the order given, without repeats; every agent if
// none are named.
func selectAgents(swarm *state.Swarm, names []string) ([]state.Agent, error) {
	if len(names) == 0 {
		return slices.Clone(swarm.Agents), nil
	}
	var agents []state.Agent
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		agent, err := swarm.Agent(name)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

func agentNames(agents []state.Agent) []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return names
}

type sendOptions struct {
	// message is nil to read the message from stdin.
	message          *string
	now, fresh, wait bool
	timeout          *float64
}

// stdinIsTerminal reports whether there's nothing piped to read: stdin is a terminal,
// not a pipe, a file, or /dev/null.
var stdinIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// readMessage is the message from -m, or else from stdin.
func readMessage(message *string) (string, error) {
	var text string
	switch {
	case message != nil:
		text = *message
	case stdinIsTerminal():
		return "", fmt.Errorf("no message; pass -m TEXT or pipe the message on stdin")
	default:
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("message is empty")
	}
	return text, nil
}

func send(swarmName string, agentArgs []string, o sendOptions) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	agents, err := selectAgents(swarm, agentArgs)
	if err != nil {
		return err
	}
	names := agentNames(agents)
	text, err := readMessage(o.message)
	if err != nil {
		return err
	}
	if o.timeout != nil && !o.wait {
		return fmt.Errorf("--timeout needs --wait")
	}
	if o.now && o.fresh {
		return fmt.Errorf("--now and --new conflict: a new session can only start a turn")
	}
	container, err := requireUp(swarm.Name)
	if err != nil {
		return err
	}
	args := append([]string{"send", text}, names...)
	if o.now {
		args = append(args, "--now")
	}
	if o.fresh {
		args = append(args, "--new")
	}
	if !o.wait {
		if err := docker(append([]string{"exec", container}, controlCommand(args...)...)...); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "queued message for %s\n", strings.Join(names, ", "))
		return nil
	}
	args = append(args, "--wait")
	if o.timeout != nil {
		args = append(args, "--timeout", formatFloat(*o.timeout))
	}
	return docker(append([]string{"exec", container}, controlCommand(args...)...)...)
}

// formatFloat is a number as Python's str(float) writes it, which the control plane parses.
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func stop(swarmName string, agentArgs []string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	agents, err := selectAgents(swarm, agentArgs)
	if err != nil {
		return err
	}
	container, err := requireUp(swarm.Name)
	if err != nil {
		return err
	}
	return docker(append([]string{"exec", container}, controlCommand(append([]string{"stop"}, agentNames(agents)...)...)...)...)
}

func wait(swarmName string, agentArgs []string, timeout *float64, anyOf bool) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	agents, err := selectAgents(swarm, agentArgs)
	if err != nil {
		return err
	}
	args := append([]string{"wait"}, agentNames(agents)...)
	if anyOf {
		args = append(args, "--any")
	}
	if timeout != nil {
		args = append(args, "--timeout", formatFloat(*timeout))
	}
	container, err := requireUp(swarm.Name)
	if err != nil {
		return err
	}
	return docker(append([]string{"exec", container}, controlCommand(args...)...)...)
}

// agentStatus is a line of `gnat-container control status`: the control plane's view of
// an agent.
type agentStatus struct {
	Agent       string         `json:"agent"`
	State       string         `json:"state"`
	Queued      int            `json:"queued"`
	Turns       int            `json:"turns"`
	Exit        *int           `json:"exit"`
	Stopped     bool           `json:"stopped"`
	SinceActive *float64       `json:"since_active"`
	Usage       map[string]any `json:"usage"`
}

// statusLine is one agent's line of `gnat status`. containerState is "" for a container
// that doesn't exist.
func statusLine(name string, s agentStatus, containerState string) string {
	agentState := s.State
	if containerState != "running" {
		// A daemon that isn't running can't be doing what the control plane last heard.
		agentState = containerState
		if agentState == "" {
			agentState = "missing"
		}
	}
	fields := []string{name, agentState, fmt.Sprintf("%d queued", s.Queued), fmt.Sprintf("%d turns", s.Turns)}
	if agentState == "busy" && s.SinceActive != nil {
		fields = append(fields, "last event "+duration(*s.SinceActive)+" ago")
	}
	if usage := render.UsageSummary(events.ParseUsage(s.Usage)); usage != "" {
		fields = append(fields, usage)
	}
	switch {
	case s.Stopped:
		fields = append(fields, "last turn stopped")
	case s.Turns > 0 && s.Exit == nil:
		fields = append(fields, "last turn interrupted (restart or down)")
	case s.Exit != nil && *s.Exit != 0:
		fields = append(fields, fmt.Sprintf("last turn exited with status %d", *s.Exit))
	}
	return strings.Join(fields, "\t")
}

// duration is a time span such as 45s, 3m12s, or 1h05m.
func duration(seconds float64) string {
	s := int(seconds)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
}

func status(swarmName string, agentArgs []string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	agents, err := selectAgents(swarm, agentArgs)
	if err != nil {
		return err
	}
	states, err := containerStates(swarm.Name)
	if err != nil {
		return err
	}
	// While the swarm is down, its logs directory on the host has what the control plane
	// last recorded.
	controlName := containerName(swarm.Name, state.Control)
	_, up := states[controlName]
	var out string
	if up {
		out, err = dockerOutput(append([]string{"exec", controlName}, controlCommand(append([]string{"status"}, agentNames(agents)...)...)...)...)
	} else {
		out, err = recordedStatus(swarm.Name, agentNames(agents), agentIDs(agents))
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s agentStatus
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return fmt.Errorf("the control plane's status isn't JSON: %s", line)
		}
		containerState := states[containerName(swarm.Name, s.Agent)]
		if !up && containerState == "" {
			containerState = "down"
		}
		fmt.Fprintln(stdout, statusLine(s.Agent, s, containerState))
	}
	return nil
}

// recordedStatus is the agents' status lines, given their IDs, from what the swarm's
// control plane last recorded on the host.
func recordedStatus(swarm string, agents []string, ids map[string]string) (string, error) {
	dir, err := state.LogsDirectory(swarm)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	err = recorded.Write(dir, agents, ids, &out)
	return out.String(), err
}

func copyOut(swarmName, source, destination string) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	name, path, ok := strings.Cut(source, ":")
	if !ok || path == "" {
		return fmt.Errorf("'%s' must be AGENT:PATH", source)
	}
	agent, err := swarm.Agent(name)
	if err != nil {
		return err
	}
	// Relative to the clone; an absolute path stays as it is.
	if !strings.HasPrefix(path, "/") {
		path = layout.ContainerWorkspace + "/" + path
	}
	// docker cp copies a directory's contents for SRC/., so keep a last "." part through
	// the cleaning.
	contents := specifiesContents(path)
	path = cleanPosix(path)
	from := path
	if contents {
		from = strings.TrimSuffix(path, "/") + "/."
	}
	container := containerName(swarm.Name, agent.Name)
	running, err := containers(swarm.Name)
	if err != nil {
		return err
	}
	if slices.Contains(running, container) {
		return docker("cp", container+":"+from, destination)
	}
	// The swarm is down, but the agent's clone and sessions are on its volumes: mount
	// them in a container that never runs, just to copy from.
	mounts, err := agentVolumes(swarm.Name, agent)
	if err != nil {
		return err
	}
	inside := false
	var places []string
	for _, m := range mounts {
		places = append(places, m.path)
		inside = inside || path == m.path || strings.HasPrefix(path, m.path+"/")
	}
	if !inside {
		return fmt.Errorf("swarm '%s' is down, so only paths under %s can be copied; `gnat up %s` for others",
			swarm.Name, strings.Join(places, ", "), swarm.Name)
	}
	existing, err := volumes(swarm.Name, agent.Name)
	if err != nil {
		return err
	}
	var options []string
	for _, m := range mounts {
		if !slices.Contains(existing, m.volume) {
			return fmt.Errorf("agent '%s' has no clone yet; it gets one at `gnat up`", agent.Name)
		}
		options = append(options, "-v", m.volume+":"+m.path)
	}
	out, err := dockerOutput(append(append([]string{"create"}, options...), agentImage)...)
	if err != nil {
		return err
	}
	temporary := strings.TrimSpace(out)
	copyErr := docker("cp", temporary+":"+from, destination)
	if err := dockerQuiet("rm", temporary); copyErr == nil {
		copyErr = err
	}
	return copyErr
}

// cleanPosix normalizes a container path the way Python's PurePosixPath keeps it: no
// repeated or trailing slashes, and no "." parts, but ".." left for the container to follow.
func cleanPosix(p string) string {
	var parts []string
	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return "/" + strings.Join(parts, "/")
}

// specifiesContents reports whether a path's last part is ".", which docker cp takes to
// mean the directory's contents rather than the directory.
func specifiesContents(p string) bool {
	p = strings.TrimRight(p, "/")
	return p == "." || strings.HasSuffix(p, "/.")
}

func logs(swarmName string, agentArgs []string, lines string, follow bool) error {
	swarm, err := state.Load(swarmName)
	if err != nil {
		return err
	}
	agents, err := selectAgents(swarm, agentArgs)
	if err != nil {
		return err
	}
	// Read from the host, so this works whether or not the swarm is up.
	dir, err := state.LogsDirectory(swarm.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	args := []string{"-n", lines}
	if follow {
		args = append(args, "-f")
	}
	for _, a := range agents {
		p := filepath.Join(dir, a.Name+".log")
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		f.Close()
		args = append(args, p)
	}
	_, err = execute(command{name: "tail", args: args})
	return err
}

func showWeb(swarmName string, open bool) error {
	if _, err := state.Load(swarmName); err != nil {
		return err
	}
	container, err := requireUp(swarmName)
	if err != nil {
		return err
	}
	url, err := webURL(container)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, url)
	if !open {
		return nil
	}
	if err := openBrowser(url); err != nil {
		return fmt.Errorf("couldn't open a browser; open the URL above")
	}
	return nil
}
