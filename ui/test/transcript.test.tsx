import { test, expect } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { Items, Tools } from "../src/transcript";
import { groupItems, type Item, type Tool } from "../src/swarm";

const tool = (summary: string, output?: string): Tool => ({
  kind: "tool",
  summary,
  output,
  items: [],
  error: false,
});

test("consecutive calls collapse into one group with original details retained", () => {
  const first = tool("Read: parser.go", "source"),
    second = tool("Bash: go test", "passed");
  const text: Item = { kind: "text", text: "Finished" };
  const groups = groupItems([first, second, text]);
  expect(groups).toEqual([[first, second], text]);
  const html = renderToStaticMarkup(<Items items={[first, second, text]} />);
  expect(html.match(/class="tool-group /g)?.length).toBe(1);
  expect(html).toContain("Called 2 tools");
  expect(html).toContain("Read: parser.go");
  expect(html).toContain("Bash: go test");
  expect(html).not.toContain(" open=");
});

test("the latest call remains visible after its result arrives without a spinner", () => {
  const html = renderToStaticMarkup(
    <Tools tools={[tool("Read: parser.go", "source")]} latest />,
  );
  expect(html).toContain("Read: parser.go");
  expect(html).not.toContain("animate-spin");
  const running = renderToStaticMarkup(
    <Tools tools={[tool("Read: parser.go")]} latest />,
  );
  expect(running).toContain("animate-spin");
});

test("messages separate tool groups and nested agent calls stay inside their parent", () => {
  const parent = tool("Agent: inspect");
  parent.items = [
    tool("Read: a"),
    tool("Read: b"),
    { kind: "text", text: "Nested result" },
  ];
  const message: Item = { kind: "message", text: "Next" };
  const last = tool("Bash: test");
  expect(groupItems([parent, message, last])).toEqual([
    [parent],
    message,
    [last],
  ]);
  const html = renderToStaticMarkup(<Items items={[parent, message, last]} />);
  expect(html.match(/class="tool-group /g)?.length).toBe(3);
  expect(html).toContain("Called 2 tools");
});

test("cancelled input shows its sender marked as cancelled before delivery", () => {
  const html = renderToStaticMarkup(
    <Items
      items={[
        {
          kind: "message",
          sender: "orchestrator",
          text: "Not started",
          cancelled: true,
        },
      ]}
    />,
  );
  expect(html).toContain("From orchestrator (cancelled before delivery):");
});
