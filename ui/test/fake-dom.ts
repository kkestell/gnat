// Just enough of the DOM for the Markdown renderer, and HTML from what it builds.
type Node = { text: string } | Element;
type Element = { tag: string; attrs: Record<string, string>; children: Node[] };

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

(globalThis as any).document = {
  createTextNode: (text: unknown) => ({ text: String(text) }),
  createElement: (tag: string) => ({
    tag, attrs: {} as Record<string, string>, children: [] as Node[],
    append(...nodes: Node[]) { this.children.push(...nodes); },
    setAttribute(name: string, value: unknown) { this.attrs[name] = String(value); },
    set className(value: string) { this.attrs.class = value; },
    set textContent(value: unknown) { this.children = [{ text: String(value) }]; },
  }),
};

export const html = (node: Node): string => "text" in node ? esc(node.text) :
  `<${node.tag}${Object.entries(node.attrs).map(([k, v]) => ` ${k}="${esc(v)}"`).join("")}>` +
  node.children.map(html).join("") + `</${node.tag}>`;
