package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/v2/store"
)

const fixture = `#!/usr/bin/env python3
import json,os,sys,subprocess,time
args=sys.argv[1:]
if args==['--version']: print('fixture');sys.exit(0)
if args==['auth','status']: print('{"loggedIn":true}');sys.exit(0)
text=sys.stdin.read()
with open(os.environ['GNAT_TEST_TRACE'],'a') as f: f.write(json.dumps({'cwd':os.getcwd(),'args':args,'text':text})+'\n')
session=args[args.index('--resume')+1] if '--resume' in args else 'fixture-session'
def emit(e): e['session_id']=session;print(json.dumps(e),flush=True)
emit({'type':'system','subtype':'init'})
if text=='block':
 child=subprocess.Popen(['sleep','1000'])
 with open(os.environ['GNAT_TEST_CHILD'],'w') as f:f.write(str(child.pid))
 time.sleep(1000)
if text=='orphan':
 child=subprocess.Popen(['sleep','1000'])
 with open(os.environ['GNAT_TEST_CHILD'],'w') as f:f.write(str(child.pid))
 sys.exit(1)
if text=='bad-resume':
 emit({'type':'result','is_error':True,'result':'No conversation found','subtype':'error_during_execution'});sys.exit(1)
emit({'type':'stream_event','event':{'delta':{'type':'text_delta','text':'hello '}}})
emit({'type':'stream_event','event':{'delta':{'type':'text_delta','text':'world'}}})
emit({'type':'assistant','message':{'content':[{'type':'text','text':'hello world'},{'type':'tool_use','id':'tool1','name':'Read','input':{'path':'file'}}]}})
emit({'type':'user','message':{'content':[{'type':'tool_result','tool_use_id':'tool1','content':'file contents'}]}})
emit({'type':'assistant','parent_tool_use_id':'tool1','message':{'content':[{'type':'text','text':'nested reply'}]}})
emit({'type':'result','is_error':False,'usage':{'input_tokens':11,'output_tokens':7},'total_cost_usd':0.01})
`

func installFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GNAT_TEST_TRACE", filepath.Join(dir, "trace"))
	t.Setenv("GNAT_TEST_CHILD", filepath.Join(dir, "child"))
	return dir
}
func fixtureClaim(dir, text, session string) *store.Claim {
	return &store.Claim{Agent: store.Agent{WorkingDir: dir, Settings: store.Settings{Harness: "claude", Model: "fixture-model", Effort: "high"}}, Text: text, SessionRef: session}
}
func TestClaudeLocalSessionsAndTranslation(t *testing.T) {
	dir := installFixture(t)
	adapter := Claude{}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := fixtureClaim(cwd, "first", "")
	if err := adapter.Probe(context.Background(), c.Agent.Settings); err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	emit := func(e map[string]any) error { events = append(events, e); return nil }
	if err := adapter.Run(context.Background(), c, emit); err != nil {
		t.Fatal(err)
	}
	if len(events) != 7 || events[0]["type"] != "session" || events[1]["type"] != "text_delta" || events[3]["type"] != "tool_call" || events[5]["parent_tool_use_id"] != "tool1" {
		t.Fatalf("normalized events: %+v", events)
	}
	if _, ok := events[6]["cache_read_input_tokens"]; ok {
		t.Fatal("missing usage became zero")
	}
	c.SessionRef = "fixture-session"
	c.Text = "second"
	if err := adapter.Run(context.Background(), c, emit); err != nil {
		t.Fatal(err)
	}
	trace, err := os.ReadFile(filepath.Join(dir, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(trace)), "\n")
	for i, line := range lines {
		var v struct {
			Cwd  string
			Args []string
			Text string
		}
		if err = json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		args := strings.Join(v.Args, " ")
		if v.Cwd != c.Agent.WorkingDir || !strings.Contains(args, "--model fixture-model --effort high") || strings.Contains(args, "--continue") {
			t.Fatal("wrong invocation", v)
		}
		if i == 1 && !strings.Contains(args, "--resume fixture-session") {
			t.Fatal("did not resume exact session")
		}
	}
	c.Text = "bad-resume"
	if err = adapter.Run(context.Background(), c, emit); err == nil || !strings.Contains(err.Error(), "No conversation found") {
		t.Fatal("failed resume silently replaced session", err)
	}
	c.Text = "callback failure"
	if err = adapter.Run(context.Background(), c, func(map[string]any) error { return errors.New("disk failure") }); err == nil || !strings.Contains(err.Error(), "disk failure") {
		t.Fatal("lost persistence failure", err)
	}
}
func TestClaudeInterruptAndOrphanCleanup(t *testing.T) {
	for _, prompt := range []string{"block", "orphan"} {
		t.Run(prompt, func(t *testing.T) {
			dir := installFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- (Claude{}).Run(ctx, fixtureClaim(dir, prompt, ""), func(map[string]any) error { return nil })
			}()
			deadline := time.Now().Add(5 * time.Second)
			var pid int
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(filepath.Join(dir, "child"))
				if err == nil {
					_, _ = fmt.Sscanf(string(data), "%d", &pid)
					if pid > 0 {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("fixture child did not start")
			}
			if prompt == "block" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected interrupted/error result")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("process group was not reaped")
			}
			// A killed grandchild may briefly be a zombie before init reaps it.
			for time.Now().Before(deadline) {
				if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("tool subprocess survived", pid)
		})
	}
}
func TestTranslatorRejectsInvalidOutput(t *testing.T) {
	tr := translator{emit: func(map[string]any) error { return nil }}
	if err := tr.line([]byte("not json")); err == nil {
		t.Fatal("invalid output accepted")
	}
	if err := (Claude{}).Probe(context.Background(), store.Settings{Harness: "codex"}); err == nil {
		t.Fatal("unavailable adapter silently accepted")
	}
}

func TestTranslatorTracksStreamedBlocksPerMessage(t *testing.T) {
	var got []map[string]any
	tr := translator{emit: func(e map[string]any) error { got = append(got, e); return nil }}
	lines := []string{
		`{"type":"stream_event","parent_tool_use_id":"nested","event":{"index":0,"delta":{"type":"text_delta","text":"streamed"}}}`,
		`{"type":"assistant","parent_tool_use_id":"nested","message":{"content":[{"type":"text","text":"streamed"},{"type":"text","text":"complete other block"}]}}`,
		`{"type":"assistant","parent_tool_use_id":"nested","message":{"content":[{"type":"text","text":"next message"}]}}`,
	}
	for _, line := range lines {
		if err := tr.line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 3 || got[0]["text"] != "streamed" || got[1]["text"] != "complete other block" || got[2]["text"] != "next message" {
		t.Fatal("duplicated or dropped assistant blocks", got)
	}
}
