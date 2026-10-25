package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/store"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--internal-daemon-run" {
		dir, err := store.StateDir()
		if err == nil {
			err = Run(dir)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDetachedDaemonLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("initial runtime supports macOS")
	}
	base := t.TempDir()
	fixture := filepath.Join(base, "claude")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\ncase \"$1\" in --version) echo fixture;; auth) echo '{\"loggedIn\":true}';; *) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", base+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	config := filepath.Join(base, "config", "gnat")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.yaml"), []byte("orchestrator: {harness: claude, model: example, effort: high}"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := store.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := Stop(ctx, dir); err != nil {
			t.Error(err)
		}
	})
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, data)
		}
	}
	git("init")
	git("commit", "--allow-empty", "-m", "initial")
	if err := os.Mkdir(filepath.Join(repo, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	// A stale descriptor is replaced only by a process that acquired the lock.
	if err := prepareRuntime(dir); err != nil {
		t.Fatal(err)
	}
	stale := Descriptor{URL: "http://127.0.0.1:1", Token: "old", Generation: "old"}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(runtimeDir(dir), "daemon.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		desc     Descriptor
		snapshot store.Snapshot
		err      error
	}
	results := make(chan result, 6)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			d, err := Ensure(ctx, dir, executable)
			var snapshot store.Snapshot
			if err == nil {
				path := []string{repo, filepath.Join(repo, "nested"), alias}[i%3]
				snapshot, err = OpenWorkspace(ctx, d, path)
			}
			results <- result{d, snapshot, err}
		})
	}
	wg.Wait()
	close(results)
	var first result
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first.desc.Generation == "" {
			first = got
		}
		if got.desc != first.desc || got.snapshot.ID != first.snapshot.ID {
			t.Fatalf("duplicate daemon/swarm: %+v %+v", first, got)
		}
	}
	for _, path := range []string{runtimeDir(dir), filepath.Join(runtimeDir(dir), "daemon.json"), filepath.Join(dir, "gnat.db")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("permissions %s: %v %v", path, info, err)
		}
	}
	// A second checkout gets its own swarm on the same listener.
	linked := filepath.Join(base, "linked")
	git("worktree", "add", "-b", "linked", linked)
	second, err := OpenWorkspace(ctx, first.desc, linked)
	if err != nil || second.ID == first.snapshot.ID {
		t.Fatalf("second workspace: %+v %v", second, err)
	}
	// An existing swarm can reopen even after required config is removed.
	if err := os.Remove(filepath.Join(config, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWorkspace(ctx, first.desc, repo); err != nil {
		t.Fatal(err)
	}
	if err := Stop(ctx, dir); err != nil {
		t.Fatal(err)
	}
	restarted, err := Ensure(ctx, dir, executable)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Generation == first.desc.Generation {
		t.Fatal("daemon did not restart")
	}
	reopened, err := OpenWorkspace(ctx, restarted, repo)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != first.snapshot.ID || reopened.Orchestrator.ID != first.snapshot.Orchestrator.ID || reopened.Orchestrator.ConversationID != first.snapshot.Orchestrator.ConversationID || reopened.Orchestrator.Settings != first.snapshot.Orchestrator.Settings || reopened.Orchestrator.WorkingDir != first.snapshot.Orchestrator.WorkingDir {
		t.Fatal("restart lost saved identity")
	}
	if err := Stop(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := Stop(ctx, dir); err != nil {
		t.Fatal("repeated stop", err)
	}
}
