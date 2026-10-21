const esc = s => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
const document = {
  createTextNode: text => ({ text: String(text) }),
  createElement: tag => ({
    tag, attrs: {}, children: [],
    append(...nodes) { this.children.push(...nodes); },
    setAttribute(name, value) { this.attrs[name] = String(value); },
    set className(value) { this.attrs.class = value; },
    set textContent(value) { this.children = [{ text: String(value) }]; },
  }),
};
const html = node => "text" in node ? esc(node.text) :
  `<${node.tag}${Object.entries(node.attrs).map(([k, v]) => ` ${k}="${esc(v)}"`).join("")}>` +
  node.children.map(html).join("") + `</${node.tag}>`;
const render = text => {
  const root = document.createElement("div");
  renderMarkdown(root, text);
  return root.children.map(html).join("");
};
