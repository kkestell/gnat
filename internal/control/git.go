package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kkestell/gnat/internal/state"
)

const (
	// mirrorName is the mirror's directory, which is also the name in the paths git
	// http-backend is given: what an agent calls the repository is not what it looks for.
	mirrorName = "repo.git"
	// syncFresh is how long a fetch from GitHub is reused by the next request.
	syncFresh = time.Second
	// gitTimeout bounds a fetch from GitHub.
	gitTimeout = 5 * time.Minute
	// symrefFresh is how long the mirror's idea of GitHub's default branch is kept.
	symrefFresh = time.Minute
	// lowSpeedLimit and lowSpeedTime make git give up on a GitHub that has all but stopped
	// answering, which would hold the mirror's lock.
	lowSpeedLimit = 1000
	lowSpeedTime  = 60
)

// preReceive is the mirror's pre-receive hook, which git runs, with what was pushed
// still quarantined, before it changes any ref. It is the rules for what an agent may
// push, and the push to GitHub itself, so a push GitHub refuses changes nothing here
// either, and the pusher is told why. Every command of the push is checked, and the
// push is refused as a whole if any fails.
//
// The rules: only branches named under GNAT_PUSH_PREFIX, which is the agent's own and
// comes from control's own environment, never from the request; no deletes; and only
// fast-forwards, of the branch as the mirror has it, which was fetched from GitHub when
// the push began. The push to GitHub is without force, so GitHub checks the last once
// more against what it has.
const preReceive = `#!/bin/sh
refuse() {
	echo "gnat: push refused: $*" >&2
	exit 1
}
[ -z "$GNAT_PUSH_REFUSAL" ] || refuse "$GNAT_PUSH_REFUSAL"
[ -n "$GNAT_PUSH_PREFIX" ] || refuse "this agent may not push"
[ -n "$GNAT_UPSTREAM" ] || refuse "no upstream"
count=0
while read -r old new ref; do
	case "$ref" in
	refs/heads/"$GNAT_PUSH_PREFIX"?*) ;;
	*) refuse "$ref is not one of your branches; you may push only to branches named ${GNAT_PUSH_PREFIX}NAME" ;;
	esac
	git check-ref-format "$ref" || refuse "$ref is not a valid branch name"
	case "$new" in
	*[!0]*) ;;
	*) refuse "deleting branches is not allowed ($ref)" ;;
	esac
	[ "$(git cat-file -t "$new" 2>/dev/null)" = commit ] || refuse "$ref must point to a commit"
	current=$(git rev-parse -q --verify "$ref" 2>/dev/null)
	case "$old" in
	*[!0]*)
		[ "$current" = "$old" ] || refuse "$ref is not where your push expects it; fetch first"
		git merge-base --is-ancestor "$old" "$new" 2>/dev/null
		case $? in
		0) ;;
		1) refuse "$ref would not be a fast-forward; force-pushing is not allowed" ;;
		*) refuse "can't tell whether $ref would be a fast-forward" ;;
		esac
		;;
	*) [ -z "$current" ] || refuse "$ref exists already; fetch first" ;;
	esac
	set -- "$@" "$new:$ref"
	count=$((count + 1))
done
[ "$count" -gt 0 ] || exit 0
# $@ now has the refspecs the loop added.
if out=$(git push --atomic "$GNAT_UPSTREAM" "$@" 2>&1); then
	echo "gnat: pushed to GitHub" >&2
else
	echo "$out" >&2
	refuse "GitHub refused the push, so nothing was changed"
fi
`

// A GitProxy is git on the swarm's network, for agents that have no GitHub credential: the
// smart HTTP protocol, served by git http-backend from a bare mirror of the swarm's
// repository, which the control plane keeps up to date from GitHub. An agent clones,
// fetches, and pushes as it would to GitHub, proving who it is with its secret as the
// password.
//
// A push is received by the mirror with the rules checked (see preReceive), sent on to
// GitHub with the control plane's token, and only then recorded in the mirror. Nothing
// in a request says which repository, which agent, or which branches are allowed.
type GitProxy struct {
	GitConfig

	who     func(secret string) string
	root    string
	gitPath string
	env     []string

	// mu is held while the mirror changes: a fetch from GitHub, and a push.
	mu sync.Mutex
	// synced is whether a fetch from GitHub has succeeded: until then the mirror is empty,
	// and nothing may be served from it.
	synced   bool
	lastSync time.Time
	lastHead time.Time
	// syncs counts the fetches from GitHub.
	syncs atomic.Int64

	// fresh is how long a fetch from GitHub is reused by the next request, and noSync
	// leaves the mirror as it is; tests change them.
	fresh  atomic.Int64
	noSync atomic.Bool
}

// GitConfig is what a GitProxy is told.
type GitConfig struct {
	// Swarm is the swarm's name, the first part of every agent's branch prefix.
	Swarm string
	// Repo is the swarm's repository, OWNER/NAME.
	Repo  string
	Token string
	// Upstream is where the repository is, https://github.com/OWNER/NAME.git if empty.
	Upstream string
	// Dir is where the mirror is kept; a new temporary directory if empty.
	Dir string
}

// NewGitProxy prepares the mirror, which has nothing in it until the first fetch.
func (c *Control) NewGitProxy(config GitConfig) (*GitProxy, error) {
	g := &GitProxy{GitConfig: config}
	g.fresh.Store(int64(syncFresh))
	if err := checkRepo(g.Repo); err != nil {
		return nil, err
	}
	if g.Upstream == "" {
		g.Upstream = "https://github.com/" + g.Repo + ".git"
	}
	g.who = c.whoIs
	var err error
	if g.gitPath, err = exec.LookPath("git"); err != nil {
		return nil, errors.New("the control plane needs git")
	}
	if g.Dir == "" {
		if g.Dir, err = os.MkdirTemp("", "gnat-git-"); err != nil {
			return nil, err
		}
	}
	g.root = g.Dir
	home := filepath.Join(g.root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	g.env = []string{
		"HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		// The control container may run as a user it has no name for.
		"GIT_COMMITTER_NAME=gnat", "GIT_COMMITTER_EMAIL=gnat@localhost",
		// Not to a GitHub that has stalled.
		fmt.Sprintf("GIT_HTTP_LOW_SPEED_LIMIT=%d", lowSpeedLimit), fmt.Sprintf("GIT_HTTP_LOW_SPEED_TIME=%d", lowSpeedTime),
	}
	if g.Token != "" {
		// Passed through the environment, not an argument, so that no process listing
		// has it; GitHub takes the token as the password of any user.
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + g.Token))
		g.env = append(g.env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth)
	}
	mirror := filepath.Join(g.root, mirrorName)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := g.git(ctx, g.root, "init", "--bare", "--quiet", mirror); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(mirror, "hooks"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(mirror, "hooks", "pre-receive"), []byte(preReceive), 0o755); err != nil {
		return nil, err
	}
	return g, nil
}

// checkRepo is an error unless repo is OWNER/NAME, as GitHub's names are.
func checkRepo(repo string) error {
	owner, name, _ := strings.Cut(repo, "/")
	if !repoRE.MatchString(repo) || strings.Trim(owner, ".") == "" || strings.Trim(name, ".") == "" {
		return fmt.Errorf("repository %q isn't OWNER/NAME", repo)
	}
	return nil
}

// git runs git in dir, and returns what it printed; its error has what it said.
func (g *GitProxy) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.gitPath, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, g.env...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errs.String()))
	}
	return out.String(), nil
}

// Sync brings the mirror up to date with GitHub: every branch and tag, and which branch is
// the default.
func (g *GitProxy) Sync(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sync(ctx)
}

// sync is Sync, with the lock held.
func (g *GitProxy) sync(ctx context.Context) error {
	if g.noSync.Load() {
		g.synced = true
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	mirror := filepath.Join(g.root, mirrorName)
	g.syncs.Add(1)
	if _, err := g.git(ctx, mirror, "fetch", "--quiet", "--prune", "--no-tags", "--no-write-fetch-head",
		"--no-recurse-submodules", g.Upstream, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return err
	}
	g.lastSync = time.Now()
	if time.Since(g.lastHead) < symrefFresh {
		g.synced = true
		return nil
	}
	out, err := g.git(ctx, mirror, "ls-remote", "--symref", g.Upstream, "HEAD")
	if err != nil {
		return err
	}
	if line, _, _ := strings.Cut(out, "\n"); strings.HasPrefix(line, "ref: refs/heads/") {
		head, _, _ := strings.Cut(strings.TrimPrefix(line, "ref: "), "\t")
		if _, err := g.git(ctx, mirror, "symbolic-ref", "HEAD", head); err != nil {
			return err
		}
	}
	g.lastHead = time.Now()
	g.synced = true
	return nil
}

// refresh updates the mirror before an agent is told what it has, unless it was updated
// just now: a loop of requests costs GitHub one fetch a second, however many, and
// GitHub, which a push is sent to without force, has the last word on whether it was
// current enough.
func (g *GitProxy) refresh(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.synced && time.Since(g.lastSync) < time.Duration(g.fresh.Load()) {
		return nil
	}
	return g.sync(ctx)
}

// isSynced is whether a fetch from GitHub has succeeded.
func (g *GitProxy) isSynced() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.synced
}

// ServeHTTP serves the smart HTTP protocol for the swarm's repository to an agent whose
// secret is the password, and nothing else.
func (g *GitProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent := ""
	if _, secret, ok := r.BasicAuth(); ok {
		agent = g.who(secret)
	}
	if agent == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="gnat"`)
		http.Error(w, "unauthorized: the password is your agent's secret ($GNAT_SECRET)", http.StatusUnauthorized)
		return
	}
	rest, ok := g.route(r.URL.Path)
	if !ok {
		g.notFound(w, r)
		return
	}
	var service string
	switch {
	case rest == "info/refs" && r.Method == http.MethodGet:
		service = r.URL.Query().Get("service")
	case rest == "git-upload-pack" && r.Method == http.MethodPost:
		service = rest
	case rest == "git-receive-pack" && r.Method == http.MethodPost:
		service = rest
	default:
		g.notFound(w, r)
		return
	}
	push := service == "git-receive-pack"
	if !push && service != "git-upload-pack" {
		// Without a service, the dumb protocol, which would serve the mirror's files.
		http.Error(w, "only the smart HTTP protocol is served", http.StatusForbidden)
		return
	}
	// An agent whose names make no valid branches can fetch, and its push says why not.
	prefix, refusal := "", ""
	if push {
		var err error
		if prefix, err = state.BranchPrefix(g.Swarm, agent); err != nil {
			refusal = err.Error()
		}
	}
	backend := r.Clone(r.Context())
	if r.ContentLength < 0 && r.Method == http.MethodPost {
		// CGI needs a body's length, which git doesn't send with a big push.
		body, size, err := g.spool(r.Body)
		if err != nil {
			http.Error(w, "reading the request: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer body.Close()
		backend.Body, backend.ContentLength, backend.TransferEncoding = body, size, nil
	}
	// Until the first fetch from GitHub, the mirror is empty, and a clone of it would be too.
	if rest == "info/refs" || !g.isSynced() {
		if err := g.refresh(r.Context()); err != nil {
			log.Printf("git: fetching %s: %v", g.Repo, err)
			http.Error(w, "the control plane couldn't fetch from GitHub: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	if push && rest != "info/refs" {
		// One push at a time, and none during a fetch from GitHub.
		g.mu.Lock()
		defer g.mu.Unlock()
	}

	env := append([]string{"PATH=" + os.Getenv("PATH")}, g.env...)
	env = append(env, "GIT_PROJECT_ROOT="+g.root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER="+agent,
		"GNAT_PUSH_PREFIX="+prefix, "GNAT_PUSH_REFUSAL="+refusal, "GNAT_UPSTREAM="+g.Upstream)
	handler := &cgi.Handler{Path: g.gitPath, Args: []string{"http-backend"}, Dir: g.root, Env: env}
	backend.URL.Path = "/" + mirrorName + "/" + rest
	// The secret is the agent's, and git has no use for it.
	backend.Header.Del("Authorization")
	handler.ServeHTTP(w, backend)
}

// spool reads a request body whole into a file, which it returns at its start with its
// size. The file is already removed, so closing it is all there is to clean up.
func (g *GitProxy) spool(body io.Reader) (*os.File, int64, error) {
	file, err := os.CreateTemp(g.root, "body-")
	if err != nil {
		return nil, 0, err
	}
	_ = os.Remove(file.Name())
	size, err := io.Copy(file, body)
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, size, nil
}

// route is what comes after the repository in a path, "" if it isn't the swarm's: always
// the one repository, however an agent spells it.
func (g *GitProxy) route(path string) (string, bool) {
	path = strings.TrimPrefix(path, "/")
	for _, prefix := range []string{g.Repo + ".git/", g.Repo + "/"} {
		if len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) {
			return path[len(prefix):], true
		}
	}
	return "", false
}

// ServeGit serves the proxy at addr until ctx is cancelled.
func ServeGit(ctx context.Context, addr string, g *GitProxy) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: g, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (g *GitProxy) notFound(w http.ResponseWriter, _ *http.Request) {
	http.NotFound(w, nil)
}
