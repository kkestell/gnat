package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gitFixture is a GitProxy serving a bare repository standing in for GitHub, with agents
// alpha, beta, and "a..b" (whose name makes no valid branch prefix).
type gitFixture struct {
	t        *testing.T
	home     string
	upstream string
	proxy    *GitProxy
	server   *httptest.Server
	github   *httptest.Server
	// pushDelay is how long GitHub takes to take a push, and fetchDelay to answer a fetch.
	pushDelay, fetchDelay atomic.Int64
}

// githubGit is git over HTTP for the repositories in root, to whoever has the control
// plane's token, as GitHub is.
func githubGit(t *testing.T, root string, pushDelay, fetchDelay *atomic.Int64) http.Handler {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+fakeToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "git-receive-pack") {
			time.Sleep(time.Duration(pushDelay.Load()))
		}
		if r.Method == http.MethodGet && r.URL.Query().Get("service") == "git-upload-pack" {
			time.Sleep(time.Duration(fetchDelay.Load()))
		}
		if r.ContentLength < 0 {
			// GitHub takes a chunked push; CGI needs its length.
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body, r.ContentLength, r.TransferEncoding = io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
		}
		(&cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{
			"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=github", "PATH=" + os.Getenv("PATH"),
		}}).ServeHTTP(w, r)
	})
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if out, err := exec.Command("git", "--exec-path").Output(); err != nil {
		t.Skip("no git")
	} else if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")); err != nil {
		t.Skip("no git http-backend")
	}
}

func newGitFixture(t *testing.T) *gitFixture { return newGitFixtureAt(t, "/upstream.git") }

// newGitFixtureAt is a fixture whose proxy fetches from GitHub at a path of it.
func newGitFixtureAt(t *testing.T, path string) *gitFixture {
	t.Helper()
	needGit(t)
	f := &gitFixture{t: t, home: t.TempDir()}
	root := t.TempDir()
	f.upstream = filepath.Join(root, "upstream.git")
	f.git(f.home, "init", "--bare", "--quiet", "-b", "main", f.upstream)
	// GitHub refuses what isn't a fast-forward unless it's forced, and the proxy never forces.
	f.git(f.upstream, "config", "receive.denyNonFastForwards", "true")
	seed := t.TempDir()
	f.git(seed, "init", "--quiet", "-b", "main")
	f.commit(seed, "README", "hello\n")
	f.git(seed, "push", "--quiet", f.upstream, "main")
	f.github = httptest.NewServer(githubGit(t, root, &f.pushDelay, &f.fetchDelay))
	t.Cleanup(f.github.Close)

	p := startPlane(t, t.TempDir(), "alpha", "beta", "a..b")
	if err := p.control.SetSecrets(map[string]string{"alpha": "alpha-secret", "beta": "beta-secret", "a..b": "dots-secret"}); err != nil {
		t.Fatal(err)
	}
	proxy, err := p.control.NewGitProxy(GitConfig{
		Swarm: "demo", Repo: "o/r", Token: fakeToken, Upstream: f.github.URL + path, Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.proxy = proxy
	proxy.fresh.Store(0) // So that what a test changes on GitHub is seen at once.
	f.server = httptest.NewServer(proxy)
	t.Cleanup(f.server.Close)
	return f
}

// run runs git in dir, as a user with no configuration but a name; the output is both
// streams, since a refused push explains itself on stderr.
func (f *gitFixture) run(dir string, args ...string) (string, error) {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+f.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *gitFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := f.run(dir, args...)
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// commit adds a commit that sets a file.
func (f *gitFixture) commit(dir, file, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", file)
	f.git(dir, "commit", "--quiet", "-m", "edit "+file)
}

// url is the repository's, with an agent's secret as its password.
func (f *gitFixture) url(secret string) string {
	return strings.Replace(f.server.URL, "http://", "http://agent:"+secret+"@", 1) + "/o/r.git"
}

// clone clones as the agent into a new directory.
func (f *gitFixture) clone(secret string) string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), "clone")
	f.git(f.home, "clone", "--quiet", f.url(secret), dir)
	return dir
}

// refs is the refs upstream has, as `git for-each-ref` shows them.
func (f *gitFixture) refs() string {
	f.t.Helper()
	return f.git(f.upstream, "for-each-ref", "--format=%(objectname) %(refname)")
}

func TestGitCloneAndPushToTheAgentsOwnBranches(t *testing.T) {
	f := newGitFixture(t)
	dir := f.clone("alpha-secret")
	if data, err := os.ReadFile(filepath.Join(dir, "README")); err != nil || string(data) != "hello\n" {
		t.Fatalf("README: %q, %v", data, err)
	}
	if branch := strings.TrimSpace(f.git(dir, "branch", "--show-current")); branch != "main" {
		t.Errorf("checked out %q", branch)
	}

	f.git(dir, "checkout", "--quiet", "-b", "demo/alpha/work")
	f.commit(dir, "a.txt", "one\n")
	out := f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")
	if !strings.Contains(out, "pushed to GitHub") {
		t.Errorf("push said %q", out)
	}
	first := strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(f.git(f.upstream, "rev-parse", "refs/heads/demo/alpha/work")); got != first {
		t.Fatalf("GitHub has %s, want %s", got, first)
	}

	// A fast-forward goes through, and so does a branch of several commits.
	f.commit(dir, "a.txt", "two\n")
	f.commit(dir, "b.txt", "x\n")
	f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")
	second := strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(f.git(f.upstream, "rev-parse", "refs/heads/demo/alpha/work")); got != second {
		t.Fatalf("GitHub has %s, want %s", got, second)
	}

	// Another agent sees it, and can work from it, but can't push to it.
	other := f.clone("beta-secret")
	f.git(other, "fetch", "--quiet", "origin")
	if got := strings.TrimSpace(f.git(other, "rev-parse", "origin/demo/alpha/work")); got != second {
		t.Errorf("beta sees %s", got)
	}
	// Work merged on GitHub, not through the control plane, is fetched too.
	seed := t.TempDir()
	f.git(seed, "clone", "--quiet", f.upstream, ".")
	f.commit(seed, "README", "changed on GitHub\n")
	f.git(seed, "push", "--quiet", "origin", "main")
	f.git(other, "fetch", "--quiet", "origin")
	if got := strings.TrimSpace(f.git(other, "show", "origin/main:README")); got != "changed on GitHub" {
		t.Errorf("beta sees %q", got)
	}
}

func TestGitRefusesWhatAnAgentMayNotPush(t *testing.T) {
	f := newGitFixture(t)
	dir := f.clone("alpha-secret")
	f.git(dir, "checkout", "--quiet", "-b", "demo/alpha/work")
	f.commit(dir, "a.txt", "one\n")
	f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")
	good := f.refs()

	// An amended commit, to force-push.
	f.git(dir, "commit", "--quiet", "--amend", "-m", "rewritten")

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"the default branch", []string{"HEAD:refs/heads/main"}, "is not one of your branches"},
		{"another agent's branch", []string{"HEAD:refs/heads/demo/beta/x"}, "is not one of your branches"},
		{"a longer agent name", []string{"HEAD:refs/heads/demo/alphabet/x"}, "is not one of your branches"},
		{"another swarm's branch", []string{"HEAD:refs/heads/other/alpha/x"}, "is not one of your branches"},
		{"the prefix without its slash", []string{"HEAD:refs/heads/demo/alpha"}, "is not one of your branches"},
		{"a tag", []string{"HEAD:refs/tags/demo/alpha/v1"}, "is not one of your branches"},
		{"another namespace", []string{"HEAD:refs/notes/demo/alpha/x"}, "is not one of your branches"},
		{"a pull request ref", []string{"HEAD:refs/pull/1/head"}, "is not one of your branches"},
		{"a deletion of its own branch", []string{":refs/heads/demo/alpha/work"}, "deleting branches is not allowed"},
		{"a deletion of another's", []string{":refs/heads/main"}, "is not one of your branches"},
		{"a force-push", []string{"--force", "HEAD:refs/heads/demo/alpha/work"}, "force-pushing is not allowed"},
		{"a force-push by plus", []string{"+HEAD:refs/heads/demo/alpha/work"}, "force-pushing is not allowed"},
		{"a force-push with a lease", []string{"--force-with-lease", "HEAD:refs/heads/demo/alpha/work"}, "force-pushing is not allowed"},
		{"one good ref with a bad one", []string{"HEAD:refs/heads/demo/alpha/fine", "HEAD:refs/heads/main"}, "is not one of your branches"},
		{"a good branch with a deletion", []string{"HEAD:refs/heads/demo/alpha/fine", ":refs/heads/demo/alpha/work"}, "deleting branches is not allowed"},
		{"a force-push among good ones", []string{"HEAD:refs/heads/demo/alpha/fine", "+HEAD:refs/heads/demo/alpha/work"}, "force-pushing is not allowed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := f.run(dir, append([]string{"push", "origin"}, c.args...)...)
			if err == nil {
				t.Fatalf("push went through:\n%s", out)
			}
			if !strings.Contains(out, "gnat: push refused") || !strings.Contains(out, c.want) {
				t.Errorf("push said:\n%s\nwant %q", out, c.want)
			}
			if got := f.refs(); got != good {
				t.Errorf("GitHub changed:\n%s\nwas:\n%s", got, good)
			}
			// Nor is anything of it left in the mirror to be fetched.
			if mirror := f.git(filepath.Join(f.proxy.root, mirrorName), "for-each-ref", "--format=%(objectname) %(refname)"); mirror != good {
				t.Errorf("the mirror changed:\n%s", mirror)
			}
		})
	}
}

func TestGitRefusesAForcePushOfWorkThatDivergedFromGitHub(t *testing.T) {
	f := newGitFixture(t)
	dir := f.clone("alpha-secret")
	f.git(dir, "checkout", "--quiet", "-b", "demo/alpha/work")
	f.commit(dir, "a.txt", "one\n")
	f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")

	// The branch moves on GitHub without the control plane's knowing. A push that is a
	// fast-forward of what the agent has is not one of what GitHub has.
	elsewhere := t.TempDir()
	f.git(elsewhere, "clone", "--quiet", "-b", "demo/alpha/work", f.upstream, ".")
	f.commit(elsewhere, "other.txt", "x\n")
	f.git(elsewhere, "push", "--quiet", "origin", "demo/alpha/work")
	moved := f.refs()

	f.commit(dir, "a.txt", "two\n")
	out, err := f.run(dir, "push", "origin", "demo/alpha/work")
	if err == nil {
		t.Fatalf("push went through:\n%s", out)
	}
	if got := f.refs(); got != moved {
		t.Errorf("GitHub changed:\n%s", got)
	}
	// Told the truth, the agent can catch up and push.
	f.git(dir, "pull", "--quiet", "--no-rebase", "--no-edit", "origin", "demo/alpha/work")
	f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")
}

func TestGitHubGetsTheLastWordOnFastForwards(t *testing.T) {
	f := newGitFixture(t)
	dir := f.clone("alpha-secret")
	f.git(dir, "checkout", "--quiet", "-b", "demo/alpha/work")
	f.commit(dir, "a.txt", "one\n")
	f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")

	// With a mirror that is behind GitHub, which a race could leave, the mirror sees a
	// fast-forward and GitHub, which is pushed to without force, does not.
	f.proxy.noSync.Store(true)
	elsewhere := t.TempDir()
	f.git(elsewhere, "clone", "--quiet", "-b", "demo/alpha/work", f.upstream, ".")
	f.commit(elsewhere, "other.txt", "x\n")
	f.git(elsewhere, "push", "--quiet", "origin", "demo/alpha/work")
	moved := f.refs()
	mirror := filepath.Join(f.proxy.root, mirrorName)
	before := f.git(mirror, "for-each-ref", "--format=%(objectname) %(refname)")

	f.commit(dir, "a.txt", "two\n")
	out, err := f.run(dir, "push", "origin", "demo/alpha/work")
	if err == nil {
		t.Fatalf("push went through:\n%s", out)
	}
	if !strings.Contains(out, "GitHub refused the push") {
		t.Errorf("push said:\n%s", out)
	}
	if got := f.refs(); got != moved {
		t.Errorf("GitHub changed:\n%s", got)
	}
	if got := f.git(mirror, "for-each-ref", "--format=%(objectname) %(refname)"); got != before {
		t.Errorf("the mirror changed:\n%s", got)
	}
}

func TestGitNeedsTheAgentsSecret(t *testing.T) {
	f := newGitFixture(t)
	clone := func(url string) (string, error) {
		return f.run(f.home, "clone", "--quiet", url, filepath.Join(t.TempDir(), "c"))
	}
	for name, url := range map[string]string{
		"no credentials":  f.server.URL + "/o/r.git",
		"a wrong secret":  f.url("alpha-secre"),
		"an empty secret": strings.Replace(f.server.URL, "http://", "http://agent:@", 1) + "/o/r.git",
		"the token":       f.url(fakeToken),
		"an agent's name": f.url("alpha"),
	} {
		if out, err := clone(url); err == nil {
			t.Errorf("%s: cloned\n%s", name, out)
		}
	}
	// Whatever the agent is called in the URL, the secret decides who it is.
	if out, err := clone(strings.Replace(f.url("beta-secret"), "agent:", "alpha:", 1)); err != nil {
		t.Errorf("a secret under another name: %v\n%s", err, out)
	}

	dir := f.clone("alpha-secret")
	f.git(dir, "checkout", "--quiet", "-b", "demo/beta/x")
	f.commit(dir, "a.txt", "one\n")
	f.git(dir, "remote", "set-url", "origin", strings.Replace(f.url("alpha-secret"), "agent:", "beta:", 1))
	if out, err := f.run(dir, "push", "origin", "demo/beta/x"); err == nil {
		t.Errorf("alpha pushed as beta:\n%s", out)
	}
	if strings.Contains(f.refs(), "demo/beta") {
		t.Errorf("GitHub has %s", f.refs())
	}
}

func TestGitServesOnlyTheSwarmsRepository(t *testing.T) {
	f := newGitFixture(t)
	for _, repo := range []string{"o/other", "x/r", "o", "o/r.git.git", "o/r%2e%2e/x"} {
		url := strings.Replace(f.server.URL, "http://", "http://agent:alpha-secret@", 1) + "/" + repo + ".git"
		if out, err := f.run(f.home, "clone", "--quiet", url, filepath.Join(t.TempDir(), "c")); err == nil {
			t.Errorf("cloned %s\n%s", repo, out)
		}
	}
	// However it's spelled.
	for _, path := range []string{"/o/r", "/o/r.git", "/O/R.git"} {
		url := strings.Replace(f.server.URL, "http://", "http://agent:alpha-secret@", 1) + path
		if out, err := f.run(f.home, "clone", "--quiet", url, filepath.Join(t.TempDir(), "c")); err != nil {
			t.Errorf("%s: %v\n%s", path, err, out)
		}
	}
}

func TestGitServesNothingButTheSmartProtocol(t *testing.T) {
	f := newGitFixture(t)
	f.clone("alpha-secret")
	get := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, f.server.URL+path, strings.NewReader(""))
		req.SetBasicAuth("agent", "alpha-secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/o/r.git/HEAD"}, {"GET", "/o/r.git/config"}, {"GET", "/o/r.git/hooks/pre-receive"},
		{"GET", "/o/r.git/objects/info/packs"}, {"GET", "/o/r.git/info/refs"}, {"GET", "/o/r.git/info/refs?service=git-upload-archive"},
		{"GET", "/o/r.git/git-upload-pack"}, {"POST", "/o/r.git/info/refs?service=git-upload-pack"},
		{"POST", "/o/r.git/git-upload-archive"}, {"GET", "/o/r.git/../../etc/passwd"}, {"GET", "/"}, {"GET", "/o/r.git/"},
		{"GET", "/o/r.git/info/refs/../../config"},
	} {
		code, body := get(c.method, c.path)
		if code != http.StatusNotFound && code != http.StatusForbidden && code != http.StatusMovedPermanently {
			t.Errorf("%s %s: %d %q", c.method, c.path, code, body)
		}
		for _, leak := range []string{"[core]", "refs/heads", "hooks", "root:"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s %s leaks %q: %q", c.method, c.path, leak, body)
			}
		}
	}
}

func TestGitLetsAgentsWhoseNamesMakeNoBranchesFetchButNotPush(t *testing.T) {
	f := newGitFixture(t)
	dir := f.clone("dots-secret")
	f.git(dir, "checkout", "--quiet", "-b", "demo/a.b/x")
	f.commit(dir, "a.txt", "one\n")
	for _, ref := range []string{"demo/a.b/x", "x"} {
		out, err := f.run(dir, "push", "origin", "HEAD:refs/heads/"+ref)
		if err == nil || !strings.Contains(out, "can't name branches") {
			t.Errorf("%s: err %v:\n%s", ref, err, out)
		}
	}
	if strings.Contains(f.refs(), "demo/a") || strings.Contains(f.refs(), "/x") {
		t.Errorf("GitHub has %s", f.refs())
	}
}

func TestGitReportsAGitHubItCantReach(t *testing.T) {
	f := newGitFixtureAt(t, "/gone.git")
	out, err := f.run(f.home, "clone", f.url("alpha-secret"), filepath.Join(t.TempDir(), "c"))
	if err == nil || !strings.Contains(out, "502") {
		t.Errorf("err %v:\n%s", err, out)
	}
}

// The hook is what checks a push, whatever git client sent it, so it is run here by hand
// with the commands a client that git itself would refuse to make could send.
func TestGitPreReceiveRefusesRefsNoRealClientWouldSend(t *testing.T) {
	f := newGitFixture(t)
	mirror := filepath.Join(f.proxy.root, mirrorName)
	dir := f.clone("alpha-secret")
	head := strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
	zero := strings.Repeat("0", 40)
	hook := func(prefix string, lines ...string) (string, error) {
		cmd := exec.Command("sh", filepath.Join(mirror, "hooks", "pre-receive"))
		cmd.Dir = mirror
		cmd.Env = append(os.Environ(), "GIT_DIR=.", "GNAT_PUSH_PREFIX="+prefix, "GNAT_UPSTREAM=http://127.0.0.1:1/nowhere.git")
		cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, ref := range []string{
		"refs/heads/demo/alpha/", "refs/heads/demo/alpha/x.lock", "refs/heads/demo/alpha/x..y", "refs/heads/demo/alpha/.x",
		"refs/heads/demo/alpha/x y", "refs/heads/demo/alpha/x~1", "refs/heads/demo/alpha/x^", "refs/heads/demo/alpha/x:y",
		"refs/heads/demo/alpha/x?", "refs/heads/demo/alpha/x*", "refs/heads/demo/alpha/x[", "refs/heads/demo/alpha/x@{1}",
		"refs/heads/demo/alpha/x\\y", "refs/heads/demo/alpha//x", "refs/heads/demo/alpha/x/", "refs/heads/demo/alpha/x.",
		"refs/heads/demo/alpha/../beta/x", "refs/heads/demo/alpha/./x",
		"refs/heads/demo/alpha", "refs/heads/demo/alphax/y", "refs/heads/demo/beta/x", "refs/heads/DEMO/alpha/x", "heads/demo/alpha/x",
		"demo/alpha/x", "refs/tags/demo/alpha/x", "refs/remotes/demo/alpha/x", "HEAD", "refs/heads/main", "",
	} {
		out, err := hook("demo/alpha/", zero+" "+head+" "+ref)
		if err == nil {
			t.Errorf("%q accepted:\n%s", ref, out)
		} else if !strings.Contains(out, "gnat: push refused:") || strings.Contains(out, "nowhere") {
			t.Errorf("%q: %s", ref, out)
		}
	}
	// Without a prefix, or with control's refusal, nothing is accepted.
	if out, err := hook("", zero+" "+head+" refs/heads/demo/alpha/x"); err == nil || strings.Contains(out, "nowhere") {
		t.Errorf("no prefix:\n%s", out)
	}
	// Reaching GitHub, which isn't there, is the one thing left for it to fail on.
	out, err := hook("demo/alpha/", zero+" "+head+" refs/heads/demo/alpha/x")
	if err == nil || !strings.Contains(out, "GitHub refused the push") {
		t.Errorf("a good ref: err %v:\n%s", err, out)
	}
}

func TestGitTakesNoPushOptions(t *testing.T) {
	f := newGitFixture(t)
	dir := f.clone("alpha-secret")
	f.git(dir, "checkout", "--quiet", "-b", "demo/alpha/work")
	f.commit(dir, "a.txt", "one\n")
	before := f.refs()
	out, err := f.run(dir, "push", "-o", "ci.skip", "origin", "demo/alpha/work")
	if err == nil || !strings.Contains(out, "push options") {
		t.Errorf("err %v:\n%s", err, out)
	}
	if f.refs() != before {
		t.Errorf("GitHub changed:\n%s", f.refs())
	}
	// And without the option, the same push goes through.
	f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")
}

func TestGitSerializesConcurrentPushes(t *testing.T) {
	f := newGitFixture(t)
	type result struct {
		name string
		out  string
		err  error
	}
	work := func(secret, branch string) func() result {
		dir := f.clone(secret)
		f.git(dir, "checkout", "--quiet", "-b", branch)
		f.commit(dir, branch[strings.LastIndex(branch, "/")+1:]+".txt", branch+"\n")
		return func() result {
			out, err := f.run(dir, "push", "--quiet", "origin", branch)
			return result{branch, out, err}
		}
	}
	var pushes []func() result
	for _, b := range []string{"demo/alpha/a", "demo/alpha/b", "demo/alpha/c"} {
		pushes = append(pushes, work("alpha-secret", b))
	}
	for _, b := range []string{"demo/beta/a", "demo/beta/b", "demo/beta/c"} {
		pushes = append(pushes, work("beta-secret", b))
	}
	results := make(chan result, len(pushes))
	for _, push := range pushes {
		go func() { results <- push() }()
	}
	for range pushes {
		if r := <-results; r.err != nil {
			t.Errorf("%s: %v\n%s", r.name, r.err, r.out)
		}
	}
	for _, b := range []string{"alpha/a", "alpha/b", "alpha/c", "beta/a", "beta/b", "beta/c"} {
		if !strings.Contains(f.refs(), "refs/heads/demo/"+b) {
			t.Errorf("GitHub lacks %s:\n%s", b, f.refs())
		}
	}
}

func TestGitLetsOnlyOneOfTwoPushesToTheSameBranchWin(t *testing.T) {
	f := newGitFixture(t)
	first, second := f.clone("alpha-secret"), f.clone("alpha-secret")
	for i, dir := range []string{first, second} {
		f.git(dir, "checkout", "--quiet", "-b", "demo/alpha/work")
		f.commit(dir, "a.txt", []string{"one\n", "two\n"}[i])
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, dir := range []string{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.run(dir, "push", "--quiet", "origin", "demo/alpha/work")
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("errors %v and %v: want exactly one push to win", errs[0], errs[1])
	}
	winner := first
	if errs[0] != nil {
		winner = second
	}
	want := strings.TrimSpace(f.git(winner, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(f.git(f.upstream, "rev-parse", "refs/heads/demo/alpha/work")); got != want {
		t.Errorf("GitHub has %s, want the winner's %s", got, want)
	}
}

// A push bigger than git's post buffer is sent chunked, without a length.
func TestGitTakesAPushTooBigToSendInOnePiece(t *testing.T) {
	f := newGitFixture(t)
	dir := f.clone("alpha-secret")
	f.git(dir, "checkout", "--quiet", "-b", "demo/alpha/work")
	big := make([]byte, 4<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(dir, "add", "big.bin")
	f.git(dir, "commit", "--quiet", "-m", "big")
	f.git(dir, "push", "--quiet", "origin", "demo/alpha/work")
	if !strings.Contains(f.refs(), "refs/heads/demo/alpha/work") {
		t.Errorf("GitHub lacks the push:\n%s", f.refs())
	}
}

func TestGitFetchesFromGitHubNoMoreOftenThanItsFreshness(t *testing.T) {
	f := newGitFixture(t)
	f.proxy.fresh.Store(int64(time.Hour))
	advertise := func(service string) int {
		req, _ := http.NewRequest("GET", f.server.URL+"/o/r.git/info/refs?service="+service, nil)
		req.SetBasicAuth("agent", "alpha-secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code := advertise([]string{"git-receive-pack", "git-upload-pack"}[i%2]); code != http.StatusOK {
				t.Errorf("status %d", code)
			}
		}()
	}
	wg.Wait()
	for range 10 {
		advertise("git-receive-pack")
	}
	if n := f.proxy.syncs.Load(); n != 1 {
		t.Errorf("fetched from GitHub %d times", n)
	}
	// Once it's stale again, the next asks for another.
	f.proxy.fresh.Store(int64(0))
	advertise("git-receive-pack")
	if n := f.proxy.syncs.Load(); n != 2 {
		t.Errorf("fetched from GitHub %d times", n)
	}
}

// A push has to wait for one that is slow, and git doesn't retry a refusal, so it waits
// rather than being turned away.
func TestGitMakesAPushWaitItsTurnRatherThanFail(t *testing.T) {
	f := newGitFixture(t)
	f.pushDelay.Store(int64(1500 * time.Millisecond))
	work := func(secret, branch string) func() error {
		dir := f.clone(secret)
		f.git(dir, "checkout", "--quiet", "-b", branch)
		f.commit(dir, "x.txt", branch+"\n")
		return func() error {
			out, err := f.run(dir, "push", "--quiet", "origin", branch)
			if err != nil {
				t.Errorf("%s:\n%s", branch, out)
			}
			return err
		}
	}
	slow, waiting := work("alpha-secret", "demo/alpha/slow"), work("beta-secret", "demo/beta/waiting")
	done := make(chan error, 2)
	go func() { done <- slow() }()
	time.Sleep(300 * time.Millisecond)
	go func() { done <- waiting() }()
	<-done
	<-done
	for _, ref := range []string{"demo/alpha/slow", "demo/beta/waiting"} {
		if !strings.Contains(f.refs(), "refs/heads/"+ref) {
			t.Errorf("GitHub lacks %s:\n%s", ref, f.refs())
		}
	}
}

// The control plane fetches the repository when it starts, and an agent that clones while
// that is still going on must get the repository, not the mirror as it is so far: empty.
func TestGitAClonePastTheFirstFetchWaitsForItAndGetsTheCommits(t *testing.T) {
	f := newGitFixture(t)
	f.fetchDelay.Store(int64(700 * time.Millisecond))
	f.proxy.fresh.Store(int64(time.Hour)) // What the clone waited for is fresh enough for it.
	started := make(chan error, 1)
	go func() { started <- f.proxy.Sync(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for f.proxy.syncs.Load() == 0 { // The fetch has the lock, and has started.
		if time.Now().After(deadline) {
			t.Fatal("the first fetch never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	dir := f.clone("alpha-secret")
	if data, err := os.ReadFile(filepath.Join(dir, "README")); err != nil || string(data) != "hello\n" {
		t.Fatalf("README: %q, %v", data, err)
	}
	if got := strings.TrimSpace(f.git(dir, "log", "--format=%s")); got == "" {
		t.Error("the clone has no commits")
	}
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if n := f.proxy.syncs.Load(); n != 1 {
		t.Errorf("fetched from GitHub %d times", n)
	}
}

// If the first fetch fails there is nothing to serve: what waited on it fails too, and
// so does what comes after, until one succeeds.
func TestGitNothingIsServedBeforeAFetchHasSucceeded(t *testing.T) {
	f := newGitFixtureAt(t, "/gone.git")
	for range 2 {
		out, err := f.run(f.home, "clone", f.url("alpha-secret"), filepath.Join(t.TempDir(), "c"))
		if err == nil || !strings.Contains(out, "502") {
			t.Errorf("err %v:\n%s", err, out)
		}
	}
	// Not by a request for the pack alone either.
	req, _ := http.NewRequest("POST", f.server.URL+"/o/r.git/git-upload-pack", strings.NewReader("0000"))
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.SetBasicAuth("agent", "alpha-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d", resp.StatusCode)
	}
}
