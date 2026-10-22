package cli

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kkestell/gnat/internal/harness"
	"github.com/kkestell/gnat/internal/layout"
	"github.com/kkestell/gnat/internal/state"
	"github.com/kkestell/gnat/internal/workspace"
)

const (
	controlImage = "gnat-control:latest"
	// The base agent image, built from the Dockerfile beside this file.
	agentImage = "gnat-agent:latest"
	// githubTokenVar is the GitHub token, which only the control container holds; gh on
	// the host reads it too.
	githubTokenVar = "GH_TOKEN"
	swarmLabel     = "gnat.name"
	// agentLabel names the agent a volume belongs to.
	agentLabel = "gnat.agent"
	// How long `gnat up` waits for the control plane to answer, and how often it asks.
	readyTimeout  = 30 * time.Second
	readyInterval = 250 * time.Millisecond
)

// controlDockerfile is the control image: the control plane is the gnat-container binary,
// which needs git for the agents' clones and pushes, and CA certificates to reach GitHub.
const controlDockerfile = `FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
`

//go:embed Dockerfile
var agentDockerfile []byte

// requiredCommands is what gnat runs in an agent container, so a Dockerfile can't break
// it, besides each harness's program.
var requiredCommands = []string{"git"}

// A stubbed clock lets tests skip the waiting.
var (
	now   = time.Now
	sleep = time.Sleep
)

func imageName(swarm string) string { return "gnat-" + swarm + ":latest" }

// agentImageFor is the image agents run on: the base image, or the swarm's Dockerfile
// built on it.
func agentImageFor(swarm *state.Swarm) string {
	if swarm.Dockerfile != "" {
		return imageName(swarm.Name)
	}
	return agentImage
}

// usedHarnesses are the harnesses the swarm's agents use, in the order they first appear.
func usedHarnesses(swarm *state.Swarm) ([]harness.Harness, error) {
	var used []harness.Harness
	seen := map[string]bool{}
	for _, agent := range swarm.Agents {
		if seen[agent.Harness] {
			continue
		}
		seen[agent.Harness] = true
		h, err := harness.Get(agent.Harness)
		if err != nil {
			return nil, err
		}
		used = append(used, h)
	}
	return used, nil
}

// missingCommands are the commands the image lacks, as an agent container would run them.
func missingCommands(image string, commands []string) ([]string, error) {
	script := `for c in "$@"; do command -v "$c" >/dev/null || echo "$c"; done`
	args := append([]string{"run", "--rm", "--entrypoint", "sh"}, agentUserOptions()...)
	args = append(append(args, image, "-c", script, "sh"), commands...)
	res, err := execute(command{name: "docker", args: args, capture: true})
	if err != nil {
		reason := "docker run failed"
		if msg := strings.TrimSpace(res.stderr); msg != "" {
			reason = msg
		}
		return nil, fmt.Errorf("can't check image %s for %s: %s", image, strings.Join(commands, ", "), reason)
	}
	return strings.Fields(res.stdout), nil
}

// agentUserOptions run as the agent in /workspace whatever USER and WORKDIR the
// Dockerfile set.
func agentUserOptions() []string {
	return []string{"--user", strconv.Itoa(layout.AgentUID), "--workdir", layout.ContainerWorkspace}
}

func networkName(swarm string) string { return "gnat-" + swarm }

func containerName(swarm, member string) string { return "gnat-" + swarm + "-" + member }

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]+`)

// A mount is a volume and where it's mounted.
type mount struct{ path, volume string }

// agentVolumes are the agent's volumes: its clone and its harness's sessions.
func agentVolumes(swarm string, agent state.Agent) ([]mount, error) {
	h, err := harness.Get(agent.Harness)
	if err != nil {
		return nil, err
	}
	prefix := containerName(swarm, agent.Name)
	mounts := []mount{{layout.ContainerWorkspace, prefix + "-workspace"}}
	for _, dir := range h.StateDirs() {
		relative := strings.TrimPrefix(dir, layout.AgentHome+"/")
		name := strings.Trim(nonAlnum.ReplaceAllString(relative, "-"), "-")
		mounts = append(mounts, mount{dir, prefix + "-" + name})
	}
	return mounts, nil
}

// dockerList is the names docker lists for a filter, one per line.
func dockerList(args ...string) ([]string, error) {
	out, err := dockerOutput(args...)
	return strings.Fields(out), err
}

// containers are the names of every container, running or not, that belongs to the swarm.
func containers(swarm string) ([]string, error) {
	return dockerList("ps", "-a", "--filter", "label="+swarmLabel+"="+swarm, "--format", "{{.Names}}")
}

// containerStates are each of the swarm's containers' Docker state: running, restarting,
// exited, and so on.
func containerStates(swarm string) (map[string]string, error) {
	out, err := dockerOutput("ps", "-a", "--filter", "label="+swarmLabel+"="+swarm,
		"--format", "{{.Names}}\t{{.State}}")
	states := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, st, ok := strings.Cut(line, "\t"); ok {
			states[name] = st
		}
	}
	return states, err
}

func networks(swarm string) ([]string, error) {
	return dockerList("network", "ls", "--filter", "label="+swarmLabel+"="+swarm, "--format", "{{.Name}}")
}

// volumes are the names of the swarm's agents' volumes, or of one agent's.
func volumes(swarm, agent string) ([]string, error) {
	args := []string{"volume", "ls", "--filter", "label=" + swarmLabel + "=" + swarm}
	if agent != "" {
		args = append(args, "--filter", "label="+agentLabel+"="+agent)
	}
	return dockerList(append(args, "--format", "{{.Name}}")...)
}

func requireDown(swarm string) error {
	names, err := containers(swarm)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("swarm '%s' is up; run `gnat down %s` first", swarm, swarm)
	}
	return nil
}

// requireUp is the name of the swarm's control container, which must be running.
func requireUp(swarm string) (string, error) {
	name := containerName(swarm, state.Control)
	names, err := containers(swarm)
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if n == name {
			return name, nil
		}
	}
	return "", fmt.Errorf("swarm '%s' is not up; run `gnat up %s`", swarm, swarm)
}

// controlCommand is the control plane's command line for a request to the running one.
func controlCommand(args ...string) []string { return layout.Command("control", args...) }

// waitUntilReady blocks until the control plane in the container answers a request.
func waitUntilReady(container, agent string) error {
	deadline := now().Add(readyTimeout)
	args := append([]string{"exec", container}, controlCommand("status", agent)...)
	for {
		res, err := execute(command{name: "docker", args: args, capture: true})
		if err == nil {
			return nil
		}
		if !now().Before(deadline) {
			reason := strings.TrimSpace(res.stderr)
			if reason == "" {
				reason = "no output"
			}
			return fmt.Errorf("the control plane didn't answer within %g seconds: %s; see `docker logs %s`",
				readyTimeout.Seconds(), reason, container)
		}
		sleep(readyInterval)
	}
}

// webURL is where the host reaches the web UI that the control container publishes.
func webURL(container string) (string, error) {
	out, err := dockerOutput("port", container, strconv.Itoa(layout.WebPort))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("docker publishes no port for the web UI of %s", container)
	}
	return "http://" + fields[0], nil
}

// loadGithubToken is the GitHub token agents clone, push, and open pull requests with;
// by default gh's.
func loadGithubToken() (string, error) {
	if token := os.Getenv(githubTokenVar); token != "" {
		return token, nil
	}
	res, err := execute(command{name: "gh", args: []string{"auth", "token"}, capture: true})
	if err != nil {
		return "", fmt.Errorf("%s is not set and `gh auth token` failed: %s", githubTokenVar, strings.TrimSpace(res.stderr))
	}
	return strings.TrimSpace(res.stdout), nil
}

// checkRepo fails now, not in every agent, if the token can't see the repository.
func checkRepo(repo, githubToken string) error {
	res, err := execute(command{
		name: "gh", args: []string{"repo", "view", repo, "--json", "name"}, capture: true,
		env: []string{githubTokenVar + "=" + githubToken},
	})
	if err != nil {
		return fmt.Errorf("can't access repository %s: %s", repo, strings.TrimSpace(res.stderr))
	}
	return nil
}

// dockerArch is the architecture of the linux binary Docker's containers run: amd64 or arm64.
func dockerArch() (string, error) {
	out, err := dockerOutput("info", "--format", "{{.Architecture}}")
	if err != nil {
		return "", err
	}
	switch arch := strings.TrimSpace(out); arch {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("docker runs containers on %q, which gnat has no binary for", arch)
	}
}

// binaryFor is the embedded linux gnat-container binary for an architecture.
var binaryFor = workspace.Binary

// agentConfig is a tar of the agent's user-level config for its harness: instructions and
// settings.
func agentConfig(swarm *state.Swarm, agent state.Agent) ([]byte, error) {
	h, err := harness.Get(agent.Harness)
	if err != nil {
		return nil, err
	}
	var teammates []string
	for _, a := range swarm.Agents {
		if a.Name != agent.Name {
			teammates = append(teammates, a.Name)
		}
	}
	branches, _ := state.BranchPrefix(swarm.Name, agent.Name) // "" if it can't push
	instructions := workspace.AgentInstructions(agent.Instructions, teammates, h.Credential(), branches)
	return workspace.ConfigArchive(h.ConfigDir(), h.ConfigFiles(instructions, agent.Model, agent.Effort), h.StateDirs())
}

// teardown removes the swarm's containers and network, and returns the removed container
// names.
func teardown(swarm string) ([]string, error) {
	names, err := containers(swarm)
	if err != nil {
		return nil, err
	}
	if len(names) > 0 {
		if err := dockerQuiet(append([]string{"rm", "-f"}, names...)...); err != nil {
			return nil, err
		}
	}
	nets, err := networks(swarm)
	if err != nil {
		return nil, err
	}
	if len(nets) > 0 {
		if err := dockerQuiet(append([]string{"network", "rm"}, nets...)...); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// setup is what the control container is told about each agent: its harness, model, and
// effort for its web UI, and whether it may merge pull requests, which the control plane
// enforces.
type setup struct {
	Harness string  `json:"harness"`
	Model   *string `json:"model"`
	Effort  *string `json:"effort"`
	Merge   bool    `json:"merge"`
}

// newSecret is a secret for an agent to prove who it is to the control plane with; tests
// replace it.
var newSecret = func() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newAgentID is an ID for a new agent (see state.Agent), random enough never to repeat.
func newAgentID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// agentIDs are the IDs of the agents that have one, by name, for the control plane to
// tell each from an earlier agent by its name.
func agentIDs(agents []state.Agent) map[string]string {
	ids := map[string]string{}
	for _, a := range agents {
		if a.ID != "" {
			ids[a.Name] = a.ID
		}
	}
	return ids
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// up starts the swarm's control plane and agents.
func up(name string) (err error) {
	swarm, err := state.Load(name)
	if err != nil {
		return err
	}
	if len(swarm.Agents) == 0 {
		return fmt.Errorf("swarm '%s' has no agents; add one with `gnat agent create %s NAME --instructions TEXT`",
			swarm.Name, swarm.Name)
	}
	if err := requireDown(swarm.Name); err != nil {
		return err
	}
	// Agents are known to GitHub by their branches' names, so they must make valid refs.
	for _, a := range swarm.Agents {
		if _, err := state.BranchPrefix(swarm.Name, a.Name); err != nil {
			return err
		}
	}
	if swarm.Dockerfile != "" && !isFile(swarm.Dockerfile) {
		return fmt.Errorf("dockerfile not found: %s", swarm.Dockerfile)
	}
	for _, f := range swarm.Files {
		if !isFile(f.Host) {
			return fmt.Errorf("file not found: %s (for %s); fix it with `gnat file delete %s %s` and `gnat file add`",
				f.Host, f.Path, swarm.Name, f.Path)
		}
	}
	used, err := usedHarnesses(swarm)
	if err != nil {
		return err
	}
	credentials := map[string]string{}
	for _, h := range used {
		if credentials[h.Credential()], err = loadCredential(h); err != nil {
			return err
		}
	}
	githubToken, err := loadGithubToken()
	if err != nil {
		return err
	}
	if err := checkRepo(swarm.Repo, githubToken); err != nil {
		return err
	}
	arch, err := dockerArch()
	if err != nil {
		return err
	}
	binary, err := binaryFor(arch)
	if err != nil {
		return err
	}
	binaryTar, err := workspace.BinaryArchive(binary)
	if err != nil {
		return err
	}
	var filesTar []byte
	if len(swarm.Files) > 0 {
		hosts := make([]string, len(swarm.Files))
		for i, f := range swarm.Files {
			hosts[i] = f.Host
		}
		if filesTar, err = workspace.FilesArchive(hosts); err != nil {
			return err
		}
	}

	defer catchInterrupt()()
	if err := dockerInput([]byte(controlDockerfile), "build", "-q", "-t", controlImage, "-"); err != nil {
		return err
	}
	if err := dockerInput(agentDockerfile, "build", "-q", "-t", agentImage, "-"); err != nil {
		return err
	}
	if swarm.Dockerfile != "" {
		image := imageName(swarm.Name)
		if err := dockerQuiet("build", "-q", "-t", image, "-f", swarm.Dockerfile, filepath.Dir(swarm.Dockerfile)); err != nil {
			return err
		}
		required := append([]string(nil), requiredCommands...)
		for _, h := range used {
			required = append(required, h.Program())
		}
		missing, err := missingCommands(image, required)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return fmt.Errorf("image built from %s lacks %s; start it with `FROM %s` to keep what gnat needs",
				swarm.Dockerfile, strings.Join(missing, ", "), strings.Split(agentImage, ":")[0])
		}
	}

	defer func() {
		if err != nil {
			interrupted.Store(false)
			if _, terr := teardown(swarm.Name); terr != nil {
				fmt.Fprintf(stderr, "error: cleaning up: %s\n", terr)
			}
		}
	}()
	// Each agent gets a fresh secret at every `up`, known only to it and the control plane.
	secrets := map[string]string{}
	for _, a := range swarm.Agents {
		if secrets[a.Name], err = newSecret(); err != nil {
			return err
		}
	}
	return start(swarm, credentials, githubToken, secrets, binaryTar, filesTar)
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// start creates the network, the control container and the agents', and starts them.
func start(swarm *state.Swarm, credentials map[string]string, githubToken string, secrets map[string]string, binaryTar, filesTar []byte) error {
	label := swarmLabel + "=" + swarm.Name
	network := networkName(swarm.Name)
	if err := dockerQuiet("network", "create", "--label", label, network); err != nil {
		return err
	}

	controlName := containerName(swarm.Name, state.Control)
	// The logs live on the host, so they outlive the container. Run as the host user so
	// the files are theirs to read, move, and delete.
	logs, err := state.LogsDirectory(swarm.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return err
	}
	setups := map[string]setup{}
	var names []string
	for _, a := range swarm.Agents {
		setups[a.Name] = setup{a.Harness, optional(a.Model), optional(a.Effort), a.Merge}
		names = append(names, a.Name)
	}
	setupsJSON, err := json.Marshal(setups)
	if err != nil {
		return err
	}
	idsJSON, err := json.Marshal(agentIDs(swarm.Agents))
	if err != nil {
		return err
	}
	secretsJSON, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	serve := controlCommand(append(append([]string{"serve"}, names...),
		"--setups", string(setupsJSON), "--ids", string(idsJSON), "--swarm", swarm.Name, "--repo", swarm.Repo)...)
	// Publish the web UI on a free host port, reachable only from this machine. The
	// control plane holds the GitHub token, and the agents' secrets, passed by name so
	// their values stay out of the docker argv.
	args := []string{"create", "--name", controlName, "--label", label,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-v", logs + ":" + layout.LogDir,
		"--network", network, "--network-alias", state.Control,
		"-p", "127.0.0.1::" + strconv.Itoa(layout.WebPort), "-e", githubTokenVar, "-e", state.SecretsVar, controlImage}
	if _, err := execute(command{name: "docker", args: append(args, serve...), quiet: true,
		env: []string{githubTokenVar + "=" + githubToken, state.SecretsVar + "=" + string(secretsJSON)}}); err != nil {
		return err
	}
	if err := dockerInput(binaryTar, "cp", "-a", "-", controlName+":/"); err != nil {
		return err
	}
	if err := dockerQuiet("start", controlName); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "started %s\n", controlName)
	// Agents retry their connection themselves; the operator's next command won't.
	if err := waitUntilReady(controlName, swarm.Agents[0].Name); err != nil {
		return err
	}

	for _, agent := range swarm.Agents {
		if err := startAgent(swarm, agent, label, network, credentials, secrets[agent.Name], binaryTar, filesTar); err != nil {
			return err
		}
	}
	url, err := webURL(controlName)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "web UI at %s\n", url)
	return nil
}

func startAgent(swarm *state.Swarm, agent state.Agent, label, network string, credentials map[string]string,
	secret string, binaryTar, filesTar []byte) error {
	name := containerName(swarm.Name, agent.Name)
	h, err := harness.Get(agent.Harness)
	if err != nil {
		return err
	}
	cmd := layout.Command("agent", agent.Name, "--repo", swarm.Repo, "--harness", h.Name())
	for i, f := range swarm.Files {
		cmd = append(cmd, "--file", workspace.StagedPath(i)+":"+f.Path)
	}
	if agent.Model != "" {
		cmd = append(cmd, "--model", agent.Model)
	}
	if agent.Effort != "" {
		cmd = append(cmd, "--effort", agent.Effort)
	}
	// The clone and the harness's sessions live on volumes, so they outlive the
	// container: the next `up` finds them and the agent carries on where it left off.
	mounts, err := agentVolumes(swarm.Name, agent)
	if err != nil {
		return err
	}
	var mountArgs []string
	for _, m := range mounts {
		if err := dockerQuiet("volume", "create", "--label", label, "--label", agentLabel+"="+agent.Name, m.volume); err != nil {
			return err
		}
		mountArgs = append(mountArgs, "-v", m.volume+":"+m.path)
	}
	// Pass the credentials by name so their values stay out of the docker argv; each agent
	// gets only its own harness's credential and its own secret, and no GitHub token. A daemon that crashes restarts in the same
	// container, keeping its clone and session.
	args := []string{"create", "--name", name, "--label", label, "--restart", "on-failure"}
	args = append(args, agentUserOptions()...)
	args = append(args, "--network", network, "--network-alias", agent.Name)
	args = append(args, mountArgs...)
	args = append(args, "-e", h.Credential(), "-e", state.SecretVar, "--entrypoint", cmd[0], agentImageFor(swarm))
	args = append(args, cmd[1:]...)
	_, err = execute(command{name: "docker", args: args, quiet: true, env: []string{
		h.Credential() + "=" + credentials[h.Credential()], state.SecretVar + "=" + secret}})
	if err != nil {
		return err
	}
	config, err := agentConfig(swarm, agent)
	if err != nil {
		return err
	}
	for _, tar := range [][]byte{config, binaryTar, filesTar} {
		if tar == nil {
			continue
		}
		if err := dockerInput(tar, "cp", "-a", "-", name+":/"); err != nil {
			return err
		}
	}
	if err := dockerQuiet("start", name); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "started %s\n", name)
	return nil
}

// catchInterrupt makes Ctrl-C end the commands `up` runs, not `up` itself, so it can tear
// down what it started. The returned function restores the default.
func catchInterrupt() func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	go func() {
		for range signals {
			interrupted.Store(true)
		}
	}()
	return func() {
		signal.Stop(signals)
		close(signals)
	}
}
