document.body = { append() {} };
const createElement = document.createElement;
document.createElement = tag => {
  const element = createElement(tag);
  element.classList = { add: name => { element.attrs.class = `${element.attrs.class ?? ""} ${name}`.trim(); } };
  element.replaceChildren = () => { element.children = []; };
  return element;
};
const SOURCES = [];
class EventSource { constructor() { SOURCES.push(this); } }
