import { describe, expect, test } from "bun:test";
import { html } from "./fake-dom";
import { renderMarkdown } from "../src/markdown.js";

// The HTML of text, rendered by the page's Markdown renderer.
function render(text: string): string {
  const root = (document as any).createElement("div");
  renderMarkdown(root, text);
  return root.children.map(html).join("");
}

test("paragraphs and inline formatting", () => {
  expect(render("a **b** *c* ~~d~~ `e`\n\nnext")).toBe(
    "<p>a <strong>b</strong> <em>c</em> <del>d</del> <code>e</code></p><p>next</p>");
});

test("underscores inside words are not emphasis", () => {
  expect(render("snake_case_name and 2 * 3 * 4")).toBe("<p>snake_case_name and 2 * 3 * 4</p>");
});

test("nested emphasis", () => {
  expect(render("***x*** **a *b***")).toBe("<p><em><strong>x</strong></em> <strong>a <em>b</em></strong></p>");
});

test("headings and rules", () => {
  expect(render("# One\n## Two ##\n---\n#nothash")).toBe("<h1>One</h1><h2>Two</h2><hr></hr><p>#nothash</p>");
});

test("fenced code is literal", () => {
  expect(render("```py\n<b>*x*</b>\n  y\n```\nafter")).toBe(
    "<pre><code>&lt;b&gt;*x*&lt;/b&gt;\n  y</code></pre><p>after</p>");
});

test("an unclosed fence runs to the end", () => {
  expect(render("```\nabc")).toBe("<pre><code>abc</code></pre>");
});

test("inline code protects markup", () => {
  expect(render("`*a* [b](https://x.y)`")).toBe("<p><code>*a* [b](https://x.y)</code></p>");
});

test("lists with nesting", () => {
  expect(render("- a\n  - b\n  - c\n- d")).toBe("<ul><li>a<ul><li>b</li><li>c</li></ul></li><li>d</li></ul>");
});

test("ordered list with start and indented nesting", () => {
  expect(render("3. a\n    - b\n4. c")).toBe(`<ol start="3"><li>a<ul><li>b</li></ul></li><li>c</li></ol>`);
});

test("a loose list wraps items in paragraphs", () => {
  expect(render("- a\n\n- b")).toBe("<ul><li><p>a</p></li><li><p>b</p></li></ul>");
});

test("code block in a list item", () => {
  expect(render("- a\n  ```\n  x\n  ```\n- b")).toBe("<ul><li>a<pre><code>x</code></pre></li><li>b</li></ul>");
});

test("blockquote", () => {
  expect(render("> a\n> **b**\n>\n> > deep")).toBe(
    "<blockquote><p>a\n<strong>b</strong></p><blockquote><p>deep</p></blockquote></blockquote>");
});

test("table with alignment", () => {
  expect(render("| a | b |\n|:-:|--:|\n| 1 | x\\|y |")).toBe(
    `<div class="table"><table><thead><tr><th class="center">a</th><th class="right">b</th></tr></thead>` +
      `<tbody><tr><td class="center">1</td><td class="right">x|y</td></tr></tbody></table></div>`);
});

test("pipes without a rule are a paragraph", () => {
  expect(render("a | b\nc | d")).toBe("<p>a | b\nc | d</p>");
});

test("hard break", () => {
  expect(render("a  \nb\\\nc")).toBe("<p>a<br></br>b<br></br>c</p>");
});

test("safe links", () => {
  expect(render(`[x](https://a.b/c_(d) "t") <mailto:a@b.c> https://e.f/g.`)).toBe(
    `<p><a href="https://a.b/c_(d)" target="_blank" rel="noopener noreferrer nofollow">x</a> ` +
      `<a href="mailto:a@b.c" target="_blank" rel="noopener noreferrer nofollow">mailto:a@b.c</a> ` +
      `<a href="https://e.f/g" target="_blank" rel="noopener noreferrer nofollow">https://e.f/g</a>.</p>`);
});

describe("unsafe URLs are not links", () => {
  for (const url of [
    "javascript:alert(1)", "JaVaScRiPt:alert(1)", " javascript:alert(1)", "data:text/html,x",
    "vbscript:x", "//evil.example", "/relative", "#frag", "java\tscript:x", "file:///etc/passwd",
  ]) {
    test(url, () => {
      const out = render(`[click](${url}) ![img](${url}) [a](<${url}>)`);
      expect(out).not.toContain("<a ");
      expect(out).not.toContain("<img");
      expect(out).not.toContain("href");
    });
  }
});

test("images are never loaded", () => {
  const out = render("![alt](https://x.y/a.png)");
  expect(out).not.toContain("<img");
  expect(out).toContain(`href="https://x.y/a.png"`);
});

test("raw HTML is text", () => {
  expect(render("<script>alert(1)</script> <img src=x onerror=alert(1)>\n\n<div>x</div>")).toBe(
    "<p>&lt;script&gt;alert(1)&lt;/script&gt; &lt;img src=x onerror=alert(1)&gt;</p>" +
      "<p>&lt;div&gt;x&lt;/div&gt;</p>");
});

test("HTML in every position is escaped", () => {
  const p = `"><svg onload=alert(1)>`;
  expect(render(`# ${p}\n- ${p}\n> ${p}\n| ${p} |\n|---|\n| ${p} |\n[${p}](https://a.b/${p})`)).not.toContain("<svg");
});

test("pathological input terminates", () => {
  const out = render("> ".repeat(500) + "x\n" + "- ".repeat(500) + "y\n" + "*a ".repeat(3000) + "\n" + "[".repeat(3000));
  expect(out).toContain("x");
});

test("oversized text is plain", () => {
  expect(render("*a*".repeat(40000)).startsWith(`<div class="plain">`)).toBe(true);
});
