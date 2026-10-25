// A small Markdown renderer. It builds DOM nodes from text only (it never parses HTML), has no raw HTML
// passthrough, and links only http(s) and mailto URLs, so nothing an agent reads can inject markup.
const SAFE_URL = /^(https?:\/\/|mailto:)/i;
const MAX_DEPTH = 12;
const MAX_LENGTH = 100000;
const BLANK = /^\s*$/;
const FENCE = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const FENCE_END = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;
const HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/;
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const QUOTE = /^ {0,3}> ?/;
const LIST = /^( *)([-*+]|\d{1,9}[.)])( +|$)(.*)$/;
const TABLE_RULE = /^:?-+:?$/;

function child(parent, tag, text) {
  const element = document.createElement(tag);
  if (text) element.textContent = text;
  parent.append(element);
  return element;
}

function indentOf(line) {
  return line.length - line.trimStart().length;
}

function startsFence(line) {
  const match = FENCE.exec(line);
  return match && !(match[1][0] === "`" && match[2].includes("`")) ? match : null;
}

// Whether a line starts a block that can interrupt a paragraph.
function interrupts(line) {
  if (startsFence(line) || HEADING.test(line) || RULE.test(line) || QUOTE.test(line)) return true;
  const item = LIST.exec(line);
  return item !== null && item[4] !== "" && (item[2] === "-" || item[2] === "*" || item[2] === "+" || item[2].startsWith("1"));
}

function splitRow(line) {
  let row = line.trim();
  if (row.startsWith("|")) row = row.slice(1);
  if (row.endsWith("|") && !row.endsWith("\\|")) row = row.slice(0, -1);
  const cells = [];
  let cell = "";
  for (let i = 0; i < row.length; i++) {
    if (row[i] === "\\" && row[i + 1] === "|") {
      cell += "|";
      i++;
    } else if (row[i] === "|") {
      cells.push(cell.trim());
      cell = "";
    } else {
      cell += row[i];
    }
  }
  cells.push(cell.trim());
  return cells;
}

// The alignments if the line after a header row makes a table, else null.
function tableRule(header, line) {
  if (line === undefined || !header.includes("|") || !line.includes("-")) return null;
  const rule = splitRow(line);
  if (!rule.every(cell => TABLE_RULE.test(cell)) || rule.length !== splitRow(header).length) return null;
  return rule.map(cell => cell.endsWith(":") ? (cell.startsWith(":") ? "center" : "right") : "");
}

function renderTable(parent, lines, i, aligns, depth) {
  const wrap = child(parent, "div");
  wrap.className = "table";
  const table = child(wrap, "table");
  const head = child(child(table, "thead"), "tr");
  splitRow(lines[i]).forEach((cell, n) => {
    const th = child(head, "th");
    if (aligns[n]) th.className = aligns[n];
    renderInline(th, cell, depth + 1, false);
  });
  const body = child(table, "tbody");
  for (i += 2; i < lines.length && !BLANK.test(lines[i]) && lines[i].includes("|"); i++) {
    const row = child(body, "tr");
    const cells = splitRow(lines[i]);
    for (let n = 0; n < aligns.length; n++) {
      const td = child(row, "td");
      if (aligns[n]) td.className = aligns[n];
      renderInline(td, cells[n] ?? "", depth + 1, false);
    }
  }
  return i;
}

function renderList(parent, lines, i, depth) {
  const first = LIST.exec(lines[i]);
  const base = first[1].length;
  const ordered = /\d/.test(first[2]);
  const list = child(parent, ordered ? "ol" : "ul");
  if (ordered && parseInt(first[2], 10) !== 1) list.setAttribute("start", String(parseInt(first[2], 10)));
  const items = [];
  let item = null;
  let loose = false;
  while (i < lines.length) {
    const line = lines[i];
    const match = LIST.exec(line);
    if (BLANK.test(line)) {
      let next = i + 1;
      while (next < lines.length && BLANK.test(lines[next])) next++;
      if (next >= lines.length || indentOf(lines[next]) < base) break;
      const sibling = LIST.exec(lines[next]);
      if (indentOf(lines[next]) < item.offset && !(sibling && /\d/.test(sibling[2]) === ordered)) break;
      item.lines.push("");
      loose = true;
      i++;
    } else if (match && match[1].length >= base && (!item || match[1].length < item.offset) && !RULE.test(line)) {
      if (/\d/.test(match[2]) !== ordered) break;
      const gap = match[3].length;
      const offset = match[1].length + match[2].length + (gap >= 1 && gap <= 4 ? gap : 1);
      const text = match[4].replace(/^\[( |x|X)\] /, (_, mark) => mark === " " ? "☐ " : "☑ ");
      item = { offset, lines: [text] };
      items.push(item);
      i++;
    } else if (item && indentOf(line) >= item.offset) {
      item.lines.push(line.slice(item.offset));
      i++;
    } else if (item && item.lines.length && item.lines[item.lines.length - 1] !== "" && !interrupts(line)) {
      item.lines.push(line.trimStart());  // a lazy continuation of the item's paragraph
      i++;
    } else {
      break;
    }
  }
  for (const { lines: body } of items) {
    while (body.length && body[body.length - 1] === "") body.pop();
    renderBlocks(child(list, "li"), body, !loose, depth + 1);
  }
  return i;
}

function renderBlocks(parent, lines, tight, depth) {
  if (depth > MAX_DEPTH) {
    child(parent, "div", lines.join("\n")).className = "plain";
    return;
  }
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    let match;
    if (BLANK.test(line)) {
      i++;
    } else if ((match = startsFence(line))) {
      const indent = indentOf(line);
      const body = [];
      for (i++; i < lines.length; i++) {
        const end = FENCE_END.exec(lines[i]);
        if (end && end[1][0] === match[1][0] && end[1].length >= match[1].length) break;
        body.push(lines[i].slice(Math.min(indent, indentOf(lines[i]))));
      }
      i++;
      child(child(parent, "pre"), "code", body.join("\n"));
    } else if ((match = HEADING.exec(line))) {
      renderInline(child(parent, "h" + match[1].length), (match[2] ?? "").trim(), 0, false);
      i++;
    } else if (RULE.test(line)) {
      child(parent, "hr");
      i++;
    } else if (QUOTE.test(line)) {
      const body = [];
      for (; i < lines.length && QUOTE.test(lines[i]); i++) body.push(lines[i].replace(QUOTE, ""));
      renderBlocks(child(parent, "blockquote"), body, false, depth + 1);
    } else if (LIST.test(line)) {
      i = renderList(parent, lines, i, depth);
    } else if ((match = tableRule(line, lines[i + 1]))) {
      i = renderTable(parent, lines, i, match, depth);
    } else {
      const start = i;
      for (i++; i < lines.length && !BLANK.test(lines[i]) && !interrupts(lines[i]); i++);
      const text = lines.slice(start, i).map(l => l.trimStart()).join("\n").trimEnd();
      renderInline(tight ? parent : child(parent, "p"), text, 0, false);
    }
  }
}

// The index of the delimiter that closes one opened before `from`, or -1.
function findClose(text, from, mark, length) {
  for (let i = from; i < text.length;) {
    const char = text[i];
    if (char === "\\") {
      i += 2;
    } else if (char === "`") {
      let run = 0;
      while (text[i + run] === "`") run++;
      const end = text.indexOf("`".repeat(run), i + run);
      i = end < 0 ? i + run : end + run;
    } else if (char === mark) {
      let run = 0;
      while (text[i + run] === mark) run++;
      const next = text[i + run];
      const closes = i > from && !/\s/.test(text[i - 1])
        && (mark !== "_" || !next || !/[A-Za-z0-9]/.test(next))
        && (length === 1 ? run === 1 : run >= length);
      if (closes) return i + run - length;
      i += run;
    } else {
      i++;
    }
  }
  return -1;
}

// Parses [label](url "title") with `start` at the "[".
function parseLink(text, start) {
  let depth = 0;
  let i = start;
  for (; i < text.length; i++) {
    if (text[i] === "\\") i++;
    else if (text[i] === "[") depth++;
    else if (text[i] === "]" && --depth === 0) break;
  }
  if (text[i + 1] !== "(") return null;
  const label = text.slice(start + 1, i);
  let j = i + 2;
  while (text[j] === " ") j++;
  let url;
  if (text[j] === "<") {
    const end = text.indexOf(">", j);
    if (end < 0) return null;
    url = text.slice(j + 1, end);
    j = end + 1;
  } else {
    const from = j;
    for (let parens = 0; j < text.length && !/\s/.test(text[j]); j++) {
      if (text[j] === "(") parens++;
      else if (text[j] === ")" && parens-- === 0) break;
    }
    url = text.slice(from, j);
  }
  while (j < text.length && /\s/.test(text[j])) j++;
  if (text[j] === '"' || text[j] === "'") {
    const end = text.indexOf(text[j], j + 1);
    if (end < 0) return null;
    j = end + 1;
    while (text[j] === " ") j++;
  }
  return text[j] === ")" ? { label, url: url.trim(), end: j + 1 } : null;
}

function link(parent, url, label, depth) {
  const anchor = child(parent, "a");
  anchor.setAttribute("href", url);
  anchor.setAttribute("target", "_blank");
  anchor.setAttribute("rel", "noopener noreferrer nofollow");
  if (label === null) anchor.textContent = url;
  else renderInline(anchor, label, depth + 1, true);
}

function renderInline(parent, text, depth, inLink) {
  if (depth > MAX_DEPTH) {
    parent.append(document.createTextNode(text));
    return;
  }
  let plain = "";
  const flush = () => {
    if (plain) parent.append(document.createTextNode(plain));
    plain = "";
  };
  let i = 0;
  while (i < text.length) {
    const char = text[i];
    let match;
    if (char === "\\") {
      const next = text[i + 1];
      if (next === "\n") {
        flush();
        child(parent, "br");
        i += 2;
      } else if (next && /[!-\/:-@\[-`{-~]/.test(next)) {
        plain += next;
        i += 2;
      } else {
        plain += char;
        i++;
      }
    } else if (char === "`") {
      let run = 0;
      while (text[i + run] === "`") run++;
      let end = text.indexOf("`".repeat(run), i + run);
      while (end >= 0 && (text[end - 1] === "`" || text[end + run] === "`")) {
        end = text.indexOf("`".repeat(run), end + 1);
      }
      if (end < 0) {
        plain += text.slice(i, i + run);
        i += run;
      } else {
        let code = text.slice(i + run, end).replace(/\n/g, " ");
        if (code.startsWith(" ") && code.endsWith(" ") && code.trim()) code = code.slice(1, -1);
        flush();
        child(parent, "code", code);
        i = end + run;
      }
    } else if (char === "\n") {
      if (/ {2,}$/.test(plain)) {
        plain = plain.replace(/ +$/, "");
        flush();
        child(parent, "br");
      } else {
        plain += "\n";
      }
      i++;
    } else if ((char === "[" || (char === "!" && text[i + 1] === "[")) && (match = parseLink(text, i + (char === "!" ? 1 : 0)))) {
      flush();
      const image = char === "!";
      if (SAFE_URL.test(match.url) && !inLink) {
        link(parent, match.url, image ? (match.label || null) : match.label, depth);
      } else if (image) {
        parent.append(document.createTextNode(match.label));
      } else {
        renderInline(parent, match.label, depth + 1, inLink);
      }
      i = match.end;
    } else if (char === "<" && (match = /^<((?:https?:\/\/|mailto:)[^\s<>]+)>/i.exec(text.slice(i, i + 2000)))) {
      flush();
      if (inLink) parent.append(document.createTextNode(match[1]));
      else link(parent, match[1], null, depth);
      i += match[0].length;
    } else if ((char === "h" || char === "H") && !inLink && !/[A-Za-z0-9]/.test(text[i - 1] ?? "")
               && (match = /^https?:\/\/[^\s<>]+/i.exec(text.slice(i, i + 2000)))) {
      let url = match[0].replace(/[.,;:!?*_~'"]+$/, "");
      while (url.endsWith(")") && url.split(")").length > url.split("(").length) url = url.slice(0, -1);
      flush();
      link(parent, url, null, depth);
      i += url.length;
    } else if (char === "*" || char === "_" || char === "~") {
      let run = 0;
      while (text[i + run] === char) run++;
      const length = Math.min(run, char === "~" ? 2 : 3);
      const after = text[i + length];
      const opens = (char !== "~" || length === 2) && after && !/\s/.test(after)
        && (char !== "_" || !/[A-Za-z0-9]/.test(text[i - 1] ?? ""));
      const end = opens ? findClose(text, i + length, char, length) : -1;
      if (end < 0) {
        plain += text.slice(i, i + run);
        i += run;
      } else {
        flush();
        let node = parent;
        if (char === "~") node = child(node, "del");
        else if (length === 3) node = child(child(node, "em"), "strong");
        else node = child(node, length === 2 ? "strong" : "em");
        renderInline(node, text.slice(i + length, end), depth + 1, inLink);
        i = end + length;
      }
    } else {
      plain += char;
      i++;
    }
  }
  flush();
}

export function renderMarkdown(parent, text) {
  if (text.length > MAX_LENGTH) {
    child(parent, "div", text).className = "plain";
    return;
  }
  renderBlocks(parent, text.split(/\r?\n/), false, 0);
}
