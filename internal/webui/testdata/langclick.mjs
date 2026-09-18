/* Drive the real owngit.js language interceptor and report where it leaves
 * the address bar.
 *
 * The script is what ships, so a source grep cannot answer the question this
 * checks: given a language link, which address does the reader end up on? The
 * defect it guards against was invisible in the markup, because the anchor was
 * already correct and the interceptor ignored it.
 *
 * Only the handful of DOM features owngit.js actually uses are implemented,
 * and only well enough to run the language section. The page under test is
 * built from the renderer's real output, which is passed in on argv, so the
 * links here are the ones the server emits.
 *
 * Input  (argv[2]): {"script": path, "currentURL": str, "links": [{lang, href}]}
 * Output (stdout) : {"address": str, "lang": str, "cookie": str}
 */

import { readFileSync } from 'node:fs';

const input = JSON.parse(process.argv[2]);

// --- a very small DOM -------------------------------------------------

class Node {
  constructor(tag, attrs = {}) {
    this.tag = tag;
    this.attrs = { ...attrs };
    this.children = [];
    this.parent = null;
    this.textContent = '';
    this.listeners = {};
    const classes = new Set();
    this.classList = {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
      toggle: (c, on) => { if (on) { classes.add(c); } else { classes.delete(c); } },
    };
  }
  setAttribute(name, value) { this.attrs[name] = String(value); }
  getAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attrs, name) ? this.attrs[name] : null; }
  removeAttribute(name) { delete this.attrs[name]; }
  hasAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attrs, name); }
  append(child) { child.parent = this; this.children.push(child); return child; }

  get descendants() {
    const out = [];
    for (const child of this.children) { out.push(child, ...child.descendants); }
    return out;
  }

  // Supports the shapes owngit.js uses: "[attr]", "tag[attr=\"v\"]",
  // "[attr=\"v\"]" and a bare tag name.
  matches(selector) {
    const m = /^([a-z]*)(?:\[([\w-]+)(?:=["']?([^"'\]]*)["']?)?\])?$/.exec(selector.trim());
    if (!m) { return false; }
    const [, tag, attr, value] = m;
    if (tag && this.tag !== tag) { return false; }
    if (!attr) { return Boolean(tag); }
    if (!this.hasAttribute(attr)) { return false; }
    return value === undefined || this.getAttribute(attr) === value;
  }
  querySelectorAll(selector) {
    return this.descendants.filter((n) => selector.split(',').some((s) => n.matches(s)));
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  closest(selector) {
    let node = this;
    while (node) {
      if (node.matches && node.matches(selector)) { return node; }
      node = node.parent;
    }
    return null;
  }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
}

const root = new Node('html', { 'data-lang': 'en', 'data-lang-cookie': 'owngit_lang' });
const body = root.append(new Node('body'));

// The language links the renderer actually produced.
for (const link of input.links) {
  body.append(new Node('a', { 'data-lang-set': link.lang, href: link.href }));
}
// A form field the switch keeps in step, as on a real page.
body.append(new Node('input', { name: 'lang', value: 'en' }));

let cookie = '';
const documentListeners = {};

globalThis.document = {
  documentElement: root,
  title: '',
  get cookie() { return cookie; },
  set cookie(value) { cookie = String(value).split(';')[0]; },
  querySelectorAll: (s) => root.querySelectorAll(s),
  querySelector: (s) => root.querySelector(s),
  addEventListener: (type, fn) => { (documentListeners[type] ||= []).push(fn); },
};

let address = input.currentURL;
globalThis.window = {
  location: {
    // The screen was rendered from a POST, so this is the POST-only route.
    get href() { return new URL(address, 'https://owngit.test').toString(); },
    protocol: 'https:',
  },
  history: {
    state: null,
    replaceState: (_state, _title, next) => { address = next; },
  },
  matchMedia: () => ({ matches: false, addEventListener() {}, addListener() {} }),
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
};
globalThis.URL = URL;

// --- run the real script ---------------------------------------------

const source = readFileSync(input.script, 'utf8');
new Function(source)();

// --- click the Korean language link ----------------------------------

const target = root.querySelector('[data-lang-set]');
const korean = root.querySelectorAll('[data-lang-set]').find((n) => n.getAttribute('data-lang-set') === 'ko') || target;

let prevented = false;
const event = {
  target: korean,
  button: 0,
  metaKey: false, ctrlKey: false, shiftKey: false, altKey: false,
  preventDefault() { prevented = true; },
};
for (const fn of documentListeners.click || []) { fn(event); }

process.stdout.write(JSON.stringify({
  address,
  prevented,
  lang: root.getAttribute('data-lang'),
  cookie,
}));
