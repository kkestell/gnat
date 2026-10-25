package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/v2/store"
)

// Exercise the real detached daemon, adapters, SQLite reopen and process groups.
func TestDetachedExecutionRecovery(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("initial runtime supports macOS")
	}
	base := t.TempDir()
	fixture := `#!/usr/bin/env python3
import sys,os,json,uuid,subprocess
args=sys.argv[1:]
if args==['--version']:print('fixture');sys.exit(0)
if args==['auth','status']:print('{"loggedIn":true}');sys.exit(0)
text=sys.stdin.read()
session=args[args.index('--resume')+1] if '--resume' in args else str(uuid.uuid4())
print(json.dumps({'type':'system','session_id':session}),flush=True)
print(json.dumps({'type':'assistant','message':{'content':[{'type':'text','text':text}]}}),flush=True)
if text.startswith('hold-'):
 child=subprocess.Popen(['sleep','300'])
 with open(os.path.join(os.environ['GNAT_TEST_RUNTIME'],text),'w') as f:json.dump([os.getpid(),child.pid],f)
 child.wait()
print(json.dumps({'type':'result','is_error':False,'session_id':session}),flush=True)
`
	if err := os.WriteFile(filepath.Join(base, "claude"), []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", base+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GNAT_TEST_RUNTIME", base)
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	config := filepath.Join(base, "config", "gnat")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.yaml"), []byte("orchestrator: {harness: claude, model: fixture, effort: high}"), 0600); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := Stop(c, dir); err != nil {
			t.Error(err)
		}
	})
	desc, err := Ensure(ctx, dir, executable)
	if err != nil {
		t.Fatal(err)
	}
	open := func(name string) store.Snapshot {
		t.Helper()
		root := filepath.Join(base, name)
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "initial"}} {
			if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git: %s %v", out, err)
			}
		}
		v, err := OpenWorkspace(ctx, desc, root)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	a, b := open("a"), open("b")
	action := func(id, key, kind, text string) store.Result {
		t.Helper()
		var result store.Result
		if err := request(ctx, desc, "POST", "/api/swarms/"+id+"/actions", map[string]string{"request_id": key, "action": kind, "text": text}, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	snapshot := func(id string) store.Snapshot {
		t.Helper()
		var v store.Snapshot
		if err := request(ctx, desc, "GET", "/api/swarms/"+id, nil, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	wait := func(id string, predicate func(store.Snapshot) bool) store.Snapshot {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			v := snapshot(id)
			if predicate(v) {
				return v
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("state did not settle: %+v", snapshot(id))
		return store.Snapshot{}
	}
	pids := func(name string) []int {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(filepath.Join(base, name))
			var p []int
			if err == nil && json.Unmarshal(data, &p) == nil {
				return p
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("missing fixture process IDs")
		return nil
	}
	gone := func(ids []int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for _, pid := range ids {
			// Ensure releases its child handle because the normal launcher exits.
			// This long-lived test must reap its own detached daemon child.
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
				time.Sleep(10 * time.Millisecond)
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Fatalf("process %d survived: %v", pid, err)
			}
		}
	}
	session := func(v store.Snapshot) string {
		t.Helper()
		if len(v.Conversations) != 1 || v.Conversations[0].SessionRef == "" {
			t.Fatal("missing saved session", v.Conversations)
		}
		return v.Conversations[0].SessionRef
	}
	action(a.ID, "first-a", "send", "hold-a")
	action(b.ID, "first-b", "send", "hold-b")
	pa, pb := pids("hold-a"), pids("hold-b")
	t.Cleanup(func() { _ = syscall.Kill(-pa[0], syscall.SIGKILL); _ = syscall.Kill(-pb[0], syscall.SIGKILL) })
	hasSession := func(v store.Snapshot) bool { return len(v.Conversations) == 1 && v.Conversations[0].SessionRef != "" }
	sa, sb := session(wait(a.ID, hasSession)), session(wait(b.ID, hasSession))
	queued := action(a.ID, "queued-a", "send", "later-a")
	action(b.ID, "queued-b", "send", "later-b")
	if err := Stop(ctx, dir); err != nil {
		t.Fatal(err)
	}
	gone(append(pa, pb...))
	desc, err = Ensure(ctx, dir, executable)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []store.Snapshot{snapshot(a.ID), snapshot(b.ID)} {
		if v.Scheduling != "paused" || len(v.Turns) != 1 || v.Turns[0].State != "interrupted" || v.Messages[0].State != "delivered" || v.Messages[1].State != "queued" {
			t.Fatal("shutdown lost execution/queue", v)
		}
	}
	if session(snapshot(a.ID)) != sa || session(snapshot(b.ID)) != sb {
		t.Fatal("reopen replaced exact sessions")
	}
	if replay := action(a.ID, "queued-a", "send", "later-a"); replay != queued {
		t.Fatal("durable retry result changed", replay, queued)
	}
	action(a.ID, "resume-a", "resume", "")
	wait(a.ID, func(v store.Snapshot) bool { return len(v.Turns) == 2 && v.Turns[1].State == "completed" })
	if v := snapshot(b.ID); v.Scheduling != "paused" || len(v.Turns) != 1 || v.Messages[1].State != "queued" {
		t.Fatal("resuming a affected b", v)
	}
	action(a.ID, "crash-turn", "send", "hold-crash")
	pc := pids("hold-crash")
	t.Cleanup(func() { _ = syscall.Kill(-pc[0], syscall.SIGKILL) })
	action(a.ID, "after-crash", "send", "after-crash")
	// Crash cleanup is deliberately the operator's responsibility, not daemon work.
	if err := syscall.Kill(desc.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	gone([]int{desc.PID})
	_ = syscall.Kill(-pc[0], syscall.SIGKILL)
	gone(pc)
	desc, err = Ensure(ctx, dir, executable)
	if err != nil {
		t.Fatal(err)
	}
	v := snapshot(a.ID)
	if v.Scheduling != "paused" || len(v.Turns) != 3 || v.Turns[2].State != "interrupted" || v.Messages[3].State != "queued" || session(v) != sa {
		t.Fatal("crash recovery lost state", v)
	}
	action(a.ID, "resume-crash", "resume", "")
	v = wait(a.ID, func(v store.Snapshot) bool { return len(v.Turns) == 4 && v.Turns[3].State == "completed" })
	if v.Turns[3].MessageID != v.Messages[3].ID || session(v) != sa {
		t.Fatal("replayed interrupted input or changed session", v)
	}
}
