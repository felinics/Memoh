package tools

// mustElementHelper is injected into every Runtime.evaluate call. It owns the
// element listing (memohInteractiveElements), snapshot-bound refs
// (memohTakeSnapshot / elementByRef / mustTarget), and the editing helpers
// used by fill, set_value, paste, and select_text.
const mustElementHelper = `
function mustElement(selector) {
  const el = document.querySelector(selector);
  if (!el) throw new Error("element not found: " + selector);
  return el;
}

const memohInteractiveSelector = [
  'a[href]',
  'button',
  'input',
  'select',
  'textarea',
  'summary',
  '[contenteditable="true"]',
  '[role="button"]',
  '[role="link"]',
  '[role="tab"]',
  '[role="menuitem"]',
  '[role="checkbox"]',
  '[role="radio"]',
  '[role="option"]',
  '[onclick]',
  '[tabindex]:not([tabindex="-1"])'
].join(',');

function memohVisible(el) {
  const rect = el.getBoundingClientRect();
  const style = getComputedStyle(el);
  if (rect.width === 0 || rect.height === 0) return null;
  if (style.visibility === 'hidden' || style.display === 'none' || Number(style.opacity) === 0) return null;
  return rect;
}

function memohRole(el) {
  const explicit = (el.getAttribute('role') || '').trim();
  if (explicit) return explicit;
  const tag = el.tagName.toLowerCase();
  if (tag === 'a') return 'link';
  if (tag === 'button') return 'button';
  if (tag === 'select') return 'combobox';
  if (tag === 'textarea') return 'textbox';
  if (tag === 'summary') return 'button';
  if (tag === 'input') {
    const type = (el.getAttribute('type') || 'text').toLowerCase();
    if (type === 'checkbox') return 'checkbox';
    if (type === 'radio') return 'radio';
    if (type === 'submit' || type === 'button' || type === 'reset') return 'button';
    return 'textbox';
  }
  return 'element';
}

function memohElementName(el) {
  const tag = el.tagName.toLowerCase();
  const type = (el.getAttribute('type') || '').toLowerCase();
  const candidates = [
    el.getAttribute('aria-label'),
    el.getAttribute('alt'),
    el.getAttribute('title'),
    el.getAttribute('placeholder')
  ];
  if (tag === 'input' && ['button', 'submit', 'reset'].includes(type)) {
    candidates.push(el.value);
  }
  candidates.push(el.innerText, el.textContent);
  for (const candidate of candidates) {
    const text = String(candidate || '').replace(/\s+/g, ' ').trim();
    if (text) return text.slice(0, 80);
  }
  return '';
}

function memohCssEscape(value) {
  if (globalThis.CSS && typeof CSS.escape === 'function') return CSS.escape(value);
  return String(value).replace(/[^a-zA-Z0-9_-]/g, '\\$&');
}

function memohCssPath(el) {
  if (el.id) return '#' + memohCssEscape(el.id);
  const parts = [];
  let node = el;
  while (node && node.nodeType === Node.ELEMENT_NODE && node !== document.body && node !== document.documentElement) {
    let part = node.tagName.toLowerCase();
    const parent = node.parentElement;
    if (!parent) break;
    const sameTag = Array.from(parent.children).filter(child => child.tagName === node.tagName);
    if (sameTag.length > 1) {
      part += ':nth-of-type(' + (sameTag.indexOf(node) + 1) + ')';
    }
    parts.unshift(part);
    node = parent;
  }
  return parts.length ? parts.join(' > ') : el.tagName.toLowerCase();
}

function memohInteractiveElements() {
  const result = [];
  const seen = new Set();
  for (const el of document.querySelectorAll(memohInteractiveSelector)) {
    if (seen.has(el)) continue;
    seen.add(el);
    const rect = memohVisible(el);
    if (!rect) continue;
    const ref = 'e' + (result.length + 1);
    result.push({
      ref,
      element: el,
      rect,
      tag: el.tagName.toLowerCase(),
      role: memohRole(el),
      name: memohElementName(el),
      selector: memohCssPath(el)
    });
  }
  return result;
}

// memohTakeSnapshot lists the interactive elements and pins them on the page
// under a snapshot id. Refs resolve through this pinned list, so a ref never
// drifts onto another element when the page changes; a navigation discards
// the list with the document.
function memohTakeSnapshot(snapshotId) {
  const items = memohInteractiveElements();
  window.__memohSnapshot = { id: String(snapshotId || ''), elements: items.map(item => item.element), taken: Date.now() };
  return items;
}

function elementByRef(ref, snapshotId) {
  const value = String(ref || '').trim().toLowerCase().replace(/^ref=/, '').replace(/^e/, '');
  const index = Number.parseInt(value, 10);
  if (!Number.isInteger(index) || index < 1) throw new Error('invalid element ref: ' + ref);
  const store = window.__memohSnapshot;
  if (!store) throw new Error('no element refs exist on this page (it was navigated or never observed); observe again');
  const expected = String(snapshotId || '');
  if (expected && store.id !== expected) throw new Error('ref ' + ref + ' belongs to snapshot ' + expected + ' but this page currently holds snapshot ' + store.id + '; observe again');
  const el = store.elements[index - 1];
  if (!el) throw new Error('element ref not found in snapshot ' + store.id + ': ' + ref + ' (observe again)');
  if (!el.isConnected) throw new Error('ref ' + ref + ' is stale: the element was removed from the page; observe again');
  return el;
}

function mustTarget(selector, ref, snapshotId) {
  if (String(ref || '').trim()) return elementByRef(ref, snapshotId);
  return mustElement(selector);
}

function memohIsField(el) {
  return el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement;
}

function memohEditableKind(el) {
  if (el instanceof HTMLSelectElement) return 'select';
  if (memohIsField(el)) {
    const type = (el.type || 'text').toLowerCase();
    if (['checkbox', 'radio', 'file', 'submit', 'button', 'reset', 'image'].includes(type)) return '';
    return 'field';
  }
  if (el.isContentEditable) return 'contenteditable';
  return '';
}

// memohSelectAll focuses an editable target and selects everything in it so a
// following Input.insertText replaces the content the way a user would.
function memohSelectAll(el) {
  const kind = memohEditableKind(el);
  if (!kind || kind === 'select') throw new Error('element is not an editable text field: ' + memohCssPath(el));
  el.focus();
  if (kind === 'field') {
    el.setSelectionRange(0, el.value.length);
    return { kind, length: el.value.length };
  }
  const range = document.createRange();
  range.selectNodeContents(el);
  const sel = window.getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  return { kind, length: (el.textContent || '').length };
}

function memohDeleteSelection(el) {
  const kind = memohEditableKind(el);
  if (kind === 'field') {
    const start = el.selectionStart, end = el.selectionEnd;
    if (start !== end) {
      el.setRangeText('', start, end, 'start');
      el.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'deleteContentBackward' }));
    }
    return el.value;
  }
  document.execCommand('delete');
  return el.textContent;
}

function memohCurrentValue(el) {
  const kind = memohEditableKind(el);
  if (kind === 'field' || kind === 'select') return el.value;
  return el.textContent;
}

// memohSetValue sets a value programmatically through the native setter (so
// framework-controlled inputs notice) and fires input/change.
function memohSetValue(el, value) {
  const kind = memohEditableKind(el);
  if (kind === 'select') {
    const option = Array.from(el.options).find(o => o.value === value);
    if (!option) throw new Error('select has no option with value ' + JSON.stringify(value));
    el.value = value;
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    return { kind, value: el.value };
  }
  if (kind === 'field') {
    const proto = el instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
    setter.call(el, value);
    el.dispatchEvent(new InputEvent('input', { bubbles: true, data: value, inputType: value === '' ? 'deleteContentBackward' : 'insertText' }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    return { kind, value: el.value };
  }
  if (kind === 'contenteditable') {
    el.textContent = value;
    el.dispatchEvent(new InputEvent('input', { bubbles: true, data: value, inputType: value === '' ? 'deleteContentBackward' : 'insertText' }));
    return { kind, value: el.textContent };
  }
  throw new Error('element is not editable: ' + memohCssPath(el));
}

// memohSelectText finds prefix+text+suffix exactly once inside an editable
// element and selects the text, or parks the caret before/after it. Offsets
// are UTF-16 code units, the unit JavaScript strings use.
function memohSelectText(el, text, prefix, suffix, mode) {
  const kind = memohEditableKind(el);
  if (!kind || kind === 'select') throw new Error('element is not an input, textarea, or contenteditable: ' + memohCssPath(el));
  const hay = kind === 'field' ? el.value : el.textContent;
  const needle = prefix + text + suffix;
  const matches = [];
  let from = 0;
  while (true) {
    const i = hay.indexOf(needle, from);
    if (i < 0) break;
    matches.push(i);
    from = i + 1;
  }
  if (matches.length === 0) throw new Error(JSON.stringify(needle) + ' was not found in the element text');
  if (matches.length > 1) throw new Error(JSON.stringify(text) + ' matches ' + matches.length + ' times; add prefix or suffix to make the match unique');
  const start = matches[0] + prefix.length;
  const end = start + text.length;
  el.focus();
  if (kind === 'field') {
    if (mode === 'cursor_before') el.setSelectionRange(start, start);
    else if (mode === 'cursor_after') el.setSelectionRange(end, end);
    else el.setSelectionRange(start, end);
    return { start, end, mode, kind, selected: el.value.slice(el.selectionStart, el.selectionEnd) };
  }
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  let offset = 0, startNode = null, startOff = 0, endNode = null, endOff = 0;
  while (walker.nextNode()) {
    const n = walker.currentNode;
    const len = n.data.length;
    if (!startNode && start <= offset + len) { startNode = n; startOff = start - offset; }
    if (!endNode && end <= offset + len) { endNode = n; endOff = end - offset; break; }
    offset += len;
  }
  if (!startNode || !endNode) throw new Error('could not map the match onto the editable content');
  const range = document.createRange();
  if (mode === 'cursor_before') { range.setStart(startNode, startOff); range.collapse(true); }
  else if (mode === 'cursor_after') { range.setStart(endNode, endOff); range.collapse(true); }
  else { range.setStart(startNode, startOff); range.setEnd(endNode, endOff); }
  const sel = window.getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  return { start, end, mode, kind, selected: sel.toString() };
}

// memohPaste dispatches a real paste event carrying text/plain (and
// text/html when given) at the target. Editors that handle paste consume it
// (defaultPrevented); the caller falls back to inserting the text otherwise.
function memohPaste(el, text, html) {
  const target = el || document.activeElement || document.body;
  if (el) el.focus();
  const dt = new DataTransfer();
  dt.setData('text/plain', text);
  if (html) dt.setData('text/html', html);
  const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: dt });
  const consumed = !target.dispatchEvent(ev);
  return { consumed, kind: memohEditableKind(target), target: memohCssPath(target) };
}

function memohInsertHTML(el, html) {
  if (el) el.focus();
  const ok = document.execCommand('insertHTML', false, html);
  return { inserted: ok };
}

function memohFocus(el) {
  el.focus();
  const active = document.activeElement;
  return { focused: active === el || (active && el.contains(active)), active: active ? memohCssPath(active) : '' };
}
`
