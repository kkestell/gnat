package web

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/fake_dom.js
var fakeDOM string

//go:embed testdata/page_dom.js
var pageDOM string

// runJS runs JavaScript that leaves its result in OUT, with node, as the Python
// tests did, and returns what it printed. It skips the test without node.
func runJS(t *testing.T, script string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("needs node to run the page's JavaScript")
	}
	cmd := exec.Command(node, "-")
	cmd.Stdin = strings.NewReader(script + "\nconsole.log(OUT)")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	return stdout.String()
}

// jsonString is the string a page script printed, such as one render's HTML.
func jsonString(t *testing.T, out string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("bad node output %q: %v", out, err)
	}
	return value
}

// render is the HTML of text, rendered by the page's Markdown renderer.
func render(t *testing.T, text string) string {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("%s\n%s\nconst OUT = JSON.stringify(render(%s));", fakeDOM, MarkdownJS, quoted)
	return jsonString(t, runJS(t, script))
}

// pageScript is the page's inline script, with its __AGENTS__ still to fill in.
func pageScript() string {
	_, script, _ := strings.Cut(Page, "<script>")
	script, _, _ = strings.Cut(script, "</script>")
	return script
}

// feed is the calls that hand the records to a page's first EventSource.
func feed(records []any) string {
	var calls strings.Builder
	for _, event := range records {
		record, _ := json.Marshal(map[string]any{"agent": "alpha", "event": event})
		data, _ := json.Marshal(string(record))
		fmt.Fprintf(&calls, "SOURCES[0].onmessage({ data: %s });\n", data)
	}
	return calls.String()
}

// show is the HTML of the alpha column after the page receives the records.
func show(t *testing.T, records []any) string {
	t.Helper()
	script := strings.Replace(pageScript(), "__AGENTS__", `[{"name": "alpha"}]`, 1)
	out := "const OUT = JSON.stringify(columns.alpha.children.map(html).join(''));"
	return jsonString(t, runJS(t, fmt.Sprintf("%s\n%s\n%s\nSOURCES[0].onopen();\n%s%s", fakeDOM, pageDOM, script, feed(records), out)))
}

func TestParagraphsAndInlineFormatting(t *testing.T) {
	assertRender(t, "a **b** *c* ~~d~~ `e`\n\nnext",
		"<p>a <strong>b</strong> <em>c</em> <del>d</del> <code>e</code></p><p>next</p>")
}

func TestUnderscoresInsideWordsAreNotEmphasis(t *testing.T) {
	assertRender(t, "snake_case_name and 2 * 3 * 4", "<p>snake_case_name and 2 * 3 * 4</p>")
}

func TestNestedEmphasis(t *testing.T) {
	assertRender(t, "***x*** **a *b***", "<p><em><strong>x</strong></em> <strong>a <em>b</em></strong></p>")
}

func TestHeadingsAndRules(t *testing.T) {
	assertRender(t, "# One\n## Two ##\n---\n#nothash",
		"<h1>One</h1><h2>Two</h2><hr></hr><p>#nothash</p>")
}

func TestFencedCodeIsLiteral(t *testing.T) {
	assertRender(t, "```py\n<b>*x*</b>\n  y\n```\nafter",
		"<pre><code>&lt;b&gt;*x*&lt;/b&gt;\n  y</code></pre><p>after</p>")
}

func TestUnclosedFenceRunsToTheEnd(t *testing.T) {
	assertRender(t, "```\nabc", "<pre><code>abc</code></pre>")
}

func TestInlineCodeProtectsMarkup(t *testing.T) {
	assertRender(t, "`*a* [b](https://x.y)`", "<p><code>*a* [b](https://x.y)</code></p>")
}

func TestListsWithNesting(t *testing.T) {
	assertRender(t, "- a\n  - b\n  - c\n- d",
		"<ul><li>a<ul><li>b</li><li>c</li></ul></li><li>d</li></ul>")
}

func TestOrderedListWithStartAndIndentedNesting(t *testing.T) {
	assertRender(t, "3. a\n    - b\n4. c",
		`<ol start="3"><li>a<ul><li>b</li></ul></li><li>c</li></ol>`)
}

func TestLooseListWrapsItemsInParagraphs(t *testing.T) {
	assertRender(t, "- a\n\n- b", "<ul><li><p>a</p></li><li><p>b</p></li></ul>")
}

func TestCodeBlockInListItem(t *testing.T) {
	assertRender(t, "- a\n  ```\n  x\n  ```\n- b",
		"<ul><li>a<pre><code>x</code></pre></li><li>b</li></ul>")
}

func TestBlockquote(t *testing.T) {
	assertRender(t, "> a\n> **b**\n>\n> > deep",
		"<blockquote><p>a\n<strong>b</strong></p><blockquote><p>deep</p></blockquote></blockquote>")
}

func TestTableWithAlignment(t *testing.T) {
	assertRender(t, "| a | b |\n|:-:|--:|\n| 1 | x\\|y |",
		`<div class="table"><table><thead><tr><th class="center">a</th><th class="right">b</th></tr></thead>`+
			`<tbody><tr><td class="center">1</td><td class="right">x|y</td></tr></tbody></table></div>`)
}

func TestPipesWithoutARuleAreAParagraph(t *testing.T) {
	assertRender(t, "a | b\nc | d", "<p>a | b\nc | d</p>")
}

func TestHardBreak(t *testing.T) {
	assertRender(t, "a  \nb\\\nc", "<p>a<br></br>b<br></br>c</p>")
}

func TestSafeLinks(t *testing.T) {
	assertRender(t, `[x](https://a.b/c_(d) "t") <mailto:a@b.c> https://e.f/g.`,
		`<p><a href="https://a.b/c_(d)" target="_blank" rel="noopener noreferrer nofollow">x</a> `+
			`<a href="mailto:a@b.c" target="_blank" rel="noopener noreferrer nofollow">mailto:a@b.c</a> `+
			`<a href="https://e.f/g" target="_blank" rel="noopener noreferrer nofollow">https://e.f/g</a>.</p>`)
}

func TestUnsafeURLsAreNotLinks(t *testing.T) {
	for _, url := range []string{
		"javascript:alert(1)", "JaVaScRiPt:alert(1)", " javascript:alert(1)", "data:text/html,x",
		"vbscript:x", "//evil.example", "/relative", "#frag", "java\tscript:x", "file:///etc/passwd",
	} {
		t.Run(url, func(t *testing.T) {
			out := render(t, fmt.Sprintf("[click](%s) ![img](%s) [a](<%s>)", url, url, url))
			if strings.Contains(out, "<a ") || strings.Contains(out, "<img") || strings.Contains(out, "href") {
				t.Errorf("unsafe url %q became a link: %s", url, out)
			}
		})
	}
}

func TestImagesAreNeverLoaded(t *testing.T) {
	out := render(t, "![alt](https://x.y/a.png)")
	if strings.Contains(out, "<img") || !strings.Contains(out, `href="https://x.y/a.png"`) {
		t.Errorf("image rendered as %s", out)
	}
}

func TestRawHTMLIsText(t *testing.T) {
	assertRender(t, "<script>alert(1)</script> <img src=x onerror=alert(1)>\n\n<div>x</div>",
		"<p>&lt;script&gt;alert(1)&lt;/script&gt; &lt;img src=x onerror=alert(1)&gt;</p>"+
			"<p>&lt;div&gt;x&lt;/div&gt;</p>")
}

func TestHTMLInEveryPositionIsEscaped(t *testing.T) {
	payload := `"><svg onload=alert(1)>`
	out := render(t, fmt.Sprintf(
		"# %s\n- %s\n> %s\n| %s |\n|---|\n| %s |\n[%s](https://a.b/%s)",
		payload, payload, payload, payload, payload, payload, payload))
	if strings.Contains(out, "<svg") {
		t.Errorf("raw HTML survived: %s", out)
	}
}

func TestPathologicalInputTerminates(t *testing.T) {
	out := render(t, strings.Repeat("> ", 500)+"x\n"+strings.Repeat("- ", 500)+"y\n"+
		strings.Repeat("*a ", 3000)+"\n"+strings.Repeat("[", 3000))
	if !strings.Contains(out, "x") {
		t.Errorf("input was lost: %s", out)
	}
}

func TestOversizedTextIsPlain(t *testing.T) {
	if out := render(t, strings.Repeat("*a*", 40000)); !strings.HasPrefix(out, `<div class="plain">`) {
		t.Errorf("oversized text rendered as %.60s", out)
	}
}

func TestPageHasTheRendererAndLoadsNothing(t *testing.T) {
	if !strings.Contains(Page, MarkdownJS) {
		t.Error("the page does not contain the Markdown renderer")
	}
	for _, needle := range []string{"innerHTML", "insertAdjacentHTML", "<script src", "<link", "@import", "http://", "https://cdn"} {
		if strings.Contains(Page, needle) {
			t.Errorf("the page contains %q", needle)
		}
	}
}

func TestToolSummariesNeverWrap(t *testing.T) {
	_, rule, _ := strings.Cut(Page, ".tool summary {")
	rule, _, _ = strings.Cut(rule, "}")
	for _, want := range []string{"white-space: nowrap", "overflow: hidden", "text-overflow: ellipsis"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the tool summary rule is missing %q", want)
		}
	}
}

func TestPageShowsEachKindOfEvent(t *testing.T) {
	got := show(t, []any{
		map[string]any{"type": "swarm_message", "text": "**go**", "sender": "beta", "new": true, "mid_turn": false},
		map[string]any{"type": "thought", "text": "hidden"},
		map[string]any{"type": "message", "text": "Let me *look*."},
		map[string]any{"type": "tool_call", "id": "t1", "name": "Task", "input": map[string]any{"description": "explore"}},
		map[string]any{"type": "message", "text": "inside", "parent": "t1"},
		map[string]any{"type": "tool_call", "id": "t2", "name": "edit", "title": "Edit a.py", "input": map[string]any{}, "parent": "t1"},
		map[string]any{"type": "tool_result", "id": "t2", "error": true, "output": "failed"},
		map[string]any{"type": "tool_result", "id": "t1", "error": false, "output": ""},
		map[string]any{"type": "turn_end", "reply": nil, "error": "rate limited"},
		map[string]any{"type": "error", "text": "Error: no auth"},
		map[string]any{"type": "swarm_done", "exit": 2, "stopped": false},
	})
	want := `<div class="notice">new session</div>` +
		`<div class="message"><div class="sender">from beta:</div><div class="md"><p><strong>go</strong></p></div></div>` +
		`<div class="text md"><p>Let me <em>look</em>.</p></div>` +
		`<details class="tool done"><summary class="">Task: explore</summary>` +
		`<div class="text md"><p>inside</p></div>` +
		`<details class="tool done error"><summary class="">edit: Edit a.py</summary><pre class="">failed</pre></details>` +
		`<pre class="">(no output)</pre></details>` +
		`<div class="notice">error: rate limited</div>` +
		`<div class="notice">Error: no auth</div>` +
		`<div class="notice">the turn failed with status 2</div>`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPageTotalsUsageInTheHeading(t *testing.T) {
	usage := map[string]any{"input_tokens": 5, "cache_read_tokens": 12300, "cache_write_tokens": 700,
		"output_tokens": 950, "cost": 0.0421}
	records := []any{
		map[string]any{"type": "usage", "input_tokens": usage["input_tokens"], "cache_read_tokens": usage["cache_read_tokens"],
			"cache_write_tokens": usage["cache_write_tokens"], "output_tokens": usage["output_tokens"], "cost": usage["cost"]},
		map[string]any{"type": "usage", "cost": 1},
		map[string]any{"type": "swarm_done", "exit": 0, "stopped": false, "usage": usage},
	}
	script := strings.Replace(pageScript(), "__AGENTS__", `[{"name": "alpha"}]`, 1)
	out := "const OUT = JSON.stringify([html(usageLabels.alpha), columns.alpha.children.map(html).join('')]);"
	full := fmt.Sprintf("%s\n%s\n%s\nSOURCES[0].onopen();\n%s%s", fakeDOM, pageDOM, script, feed(records), out)
	var got []string
	if err := json.Unmarshal([]byte(runJS(t, full)), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{`<span class="usage">13k in (12.3k cached), 950 out, $1.04</span>`, ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPageHeadsEachColumnWithTheAgentsHarnessModelAndEffort(t *testing.T) {
	script := strings.Replace(pageScript(), "__AGENTS__",
		`[{"name": "alpha", "harness": "claude", "model": "haiku", "effort": "low"},`+
			`{"name": "gamma", "harness": "ox", "model": null, "effort": null}]`, 1)
	dom := strings.Replace(pageDOM, "document.body = { append() {} };",
		"document.body = { append(section) { sections.push(section); } };", 1)
	out := "const OUT = JSON.stringify(sections.map(s => html(s.children[0])));"
	var got []string
	if err := json.Unmarshal([]byte(runJS(t, fmt.Sprintf("%s\nconst sections = [];\n%s\n%s\n%s", fakeDOM, dom, script, out))), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`<h2><span class="">alpha</span><span class="setup">claude · haiku · low effort</span><span class="usage"></span></h2>`,
		`<h2><span class="">gamma</span><span class="setup">ox</span><span class="usage"></span></h2>`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

type fakeEvents struct {
	agents []string
	setups map[string]Setup
}

func (f fakeEvents) Agents() []string               { return f.agents }
func (f fakeEvents) Setups() map[string]Setup       { return f.setups }
func (f fakeEvents) Watch() (<-chan string, func()) { return nil, func() {} }

func TestPageIsServedWithEachAgentsSetup(t *testing.T) {
	model := "haiku"
	events := fakeEvents{
		agents: []string{"alpha", "beta"},
		setups: map[string]Setup{"alpha": {Harness: "claude", Model: &model}},
	}
	served := page(events)
	prefix := Page[:strings.Index(Page, "__AGENTS__")]
	rest := served[len(prefix):]
	agents, _, _ := strings.Cut(rest, ")")
	var got []map[string]any
	if err := json.Unmarshal([]byte(agents), &got); err != nil {
		t.Fatalf("bad agents JSON %q: %v", agents, err)
	}
	want := []map[string]any{
		{"harness": "claude", "model": "haiku", "effort": nil, "name": "alpha"},
		{"name": "beta"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestPageDoesNotEscapeHTMLInSetups(t *testing.T) {
	// json.dumps, which the Python page used, does not escape HTML.
	model := "a<b>&c"
	events := fakeEvents{
		agents: []string{"alpha"},
		setups: map[string]Setup{"alpha": {Harness: "claude", Model: &model}},
	}
	if served := page(events); !strings.Contains(served, `"model":"a<b>&c"`) {
		t.Error("the page's agents escaped HTML")
	}
}

func TestPageSkipsRecordsFromOlderVersions(t *testing.T) {
	got := show(t, []any{
		"a line Claude printed",
		map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "old"}}}},
		map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "x"}}}},
		map[string]any{"type": "result", "is_error": true, "result": "old error"},
		map[string]any{"type": "message", "text": "new"},
	})
	if want := `<div class="text md"><p>new</p></div>`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// watchingEvents is Events with a hand-fed stream, for the HTTP tests.
type watchingEvents struct {
	agents  []string
	setups  map[string]Setup
	records chan string
	stopped chan struct{}
}

func (w *watchingEvents) Agents() []string         { return w.agents }
func (w *watchingEvents) Setups() map[string]Setup { return w.setups }
func (w *watchingEvents) Watch() (<-chan string, func()) {
	return w.records, func() { close(w.stopped) }
}

func TestHandlerServesThePageAndTheEventStream(t *testing.T) {
	model := "haiku"
	events := &watchingEvents{
		agents:  []string{"alpha"},
		setups:  map[string]Setup{"alpha": {Harness: "claude", Model: &model}},
		records: make(chan string, 1),
		stopped: make(chan struct{}),
	}
	server := httptest.NewServer(Handler(events))
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Errorf("GET / content type = %q", contentType)
	}
	if string(body) != page(events) {
		t.Error("GET / did not serve the page")
	}

	response, err = http.Get(server.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound || string(body) != "not found\n" {
		t.Errorf("GET /nope = %d %q", response.StatusCode, body)
	}

	response, err = http.Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Errorf("GET /events content type = %q", contentType)
	}
	reader := bufio.NewReader(response.Body)
	record := `{"agent":"alpha","event":{"type":"message","text":"hi"}}`
	events.records <- record
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "data: "+record+"\n" {
		t.Errorf("stream sent %q", line)
	}
	if blank, _ := reader.ReadString('\n'); blank != "\n" {
		t.Errorf("stream did not end its event with a blank line: %q", blank)
	}
	response.Body.Close()
	select {
	case <-events.stopped:
	case <-time.After(time.Second):
		t.Error("the disconnecting browser did not stop the watch")
	}
}

func assertRender(t *testing.T, text, want string) {
	t.Helper()
	if got := render(t, text); got != want {
		t.Errorf("render(%q):\ngot  %s\nwant %s", text, got, want)
	}
}
