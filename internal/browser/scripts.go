package browser

import (
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const refAttribute = "data-aicodingtool-ref"

// maxSnapshotElements: a page is outside the app's trust boundary, so it cannot make one snapshot
// grow without limit.
const maxSnapshotElements = 1_000

const actionableSelector = `a[href],button,input,select,textarea,summary,[role=button],[role=link],[role=tab],[role=menuitem],[role=textbox],[contenteditable=""],[contenteditable=true]`

// elementLabel is shared by snapshots and element searches: password values are never a source of a label.
const elementLabel = `node.getAttribute('aria-label') || node.getAttribute('placeholder') || node.getAttribute('title')
  || node.getAttribute('alt') || Array.from(node.labels || []).map((label) => label.innerText || label.textContent || '').join(' ').trim()
  || (node.innerText || (node.tagName === 'INPUT' && node.type === 'password' ? '' : node.value) || '').trim() || node.getAttribute('name') || ''`

// quoted is JSON.stringify of a string.
func quoted(text string) string {
	data, err := jsonx.Marshal(text)
	if err != nil {
		return `""`
	}
	return string(data)
}

func jsNumber(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

// snapshotScript reads what a caller can act on, keeping the refs it hands out on the elements themselves.
func snapshotScript(limit float64) string {
	return `(() => {
  const limit = ` + jsNumber(limit) + `;
  const selector = '` + actionableSelector + `';
  const elements = [];
  let ref = 0;
  for (const node of document.querySelectorAll(selector)) {
    if (node.disabled) continue;
    const rect = node.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) continue;
    const style = getComputedStyle(node);
    if (style.visibility === 'hidden' || style.opacity === '0') continue;
    node.setAttribute('` + refAttribute + `', String(++ref));
    const tag = node.tagName.toLowerCase();
    const type = tag === 'input' ? (node.getAttribute('type') || 'text').toLowerCase() : '';
    const label = ` + elementLabel + `;
    const element = { ref: String(ref), role: node.getAttribute('role') || (type ? tag + ':' + type : tag), name: label.replace(/\s+/g, ' ').slice(0, 140) };
    if (type !== 'password' && typeof node.value === 'string' && node.value) element.value = node.value.slice(0, 140);
    elements.push(element);
    if (elements.length >= ` + strconv.Itoa(maxSnapshotElements) + `) break;
  }
  const text = (document.body ? document.body.innerText : '').replace(/\n{3,}/g, '\n\n').trim();
  return { url: location.href, title: document.title, text: text.slice(0, limit), truncated: text.length > limit, elements };
})()`
}

// mouseNavigationScript gives a page the mouse's side buttons, which the web view acts on neither of itself.
const mouseNavigationScript = `(() => {
  const go = (event) => {
    if (event.button !== 3 && event.button !== 4) return;
    event.preventDefault();
    if (event.type === 'mouseup') history[event.button === 3 ? 'back' : 'forward']();
  };
  addEventListener('mousedown', go, true);
  addEventListener('mouseup', go, true);
})()`

func actionScript(action domain.BrowserAction) string {
	var reference string
	switch action := action.(type) {
	case domain.BrowserActionClick:
		reference = action.Ref
	case domain.BrowserActionType:
		reference = action.Ref
	}
	ref := quoted(reference)
	found := `const node = document.querySelector('[` + refAttribute + `=' + JSON.stringify(` + ref + `) + ']');
    if (!node) return 'No element has the ref ' + ` + ref + ` + '. Take a fresh snapshot first.';
    node.scrollIntoView({ block: 'center', inline: 'center' });`
	typed, ok := action.(domain.BrowserActionType)
	if !ok {
		return `(() => { ` + found + `
      node.click();
      return 'Clicked ' + (node.innerText || node.getAttribute('aria-label') || node.tagName).trim().slice(0, 80);
    })()`
	}
	text := quoted(typed.Text)
	submit := ""
	if typed.Submit != nil && *typed.Submit {
		submit = `if (node.form) node.form.requestSubmit ? node.form.requestSubmit() : node.form.submit();
    else node.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));`
	}
	return `(() => { ` + found + `
    node.focus();
    const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(node), 'value')?.set;
    if (setter) setter.call(node, ` + text + `); else node.textContent = ` + text + `;
    node.dispatchEvent(new Event('input', { bubbles: true }));
    node.dispatchEvent(new Event('change', { bubbles: true }));
    ` + submit + `
    return 'Typed into ' + (node.getAttribute('aria-label') || node.getAttribute('placeholder') || node.tagName);
  })()`
}

// pageConditionScript answers whether a page shows, or no longer shows, the text or element sought.
func pageConditionScript(condition domain.BrowserWaitCondition, value string) string {
	sought := quoted(strings.ToLower(value))
	selector := quoted(actionableSelector)
	found := `Array.from(document.querySelectorAll(` + selector + `)).some((node) => {
    const label = ` + elementLabel + `;
    return String(label).toLocaleLowerCase().includes(` + sought + `);
  })`
	switch condition {
	case domain.BrowserWaitConditionText, domain.BrowserWaitConditionTextGone:
		present := `(document.body?.innerText || '').toLocaleLowerCase().includes(` + sought + `)`
		if condition == domain.BrowserWaitConditionText {
			return present
		}
		return "!" + present
	case domain.BrowserWaitConditionElement:
		return found
	}
	return "!(" + found + ")"
}

// guardedScript also checks inside the document, since navigation can precede queued script
// execution. Nil origins run the script anywhere.
func guardedScript(script string, origins []string) string {
	if origins == nil {
		return script
	}
	list, err := jsonx.Marshal(origins)
	if err != nil {
		list = []byte("[]")
	}
	return `(() => { if (!` + string(list) + `.includes(location.origin)) throw new Error('This site needs browser approval.'); return (` + script + `); })()`
}

// countMatchesScript counts how many times the page's text holds the query, ignoring case.
func countMatchesScript(query string) string {
	return `(() => { const text = (document.body ? document.body.innerText : '').toLocaleLowerCase(); const sought = ` + quoted(strings.ToLower(query)) + `; if (!sought) return 0; let count = 0; let at = text.indexOf(sought); while (at !== -1) { count += 1; at = text.indexOf(sought, at + sought.length); } return count; })()`
}

const clearSelectionScript = `getSelection().removeAllRanges()`

const pageSizeScript = `({ width: Math.max(document.documentElement.scrollWidth, innerWidth), height: Math.max(document.documentElement.scrollHeight, document.body ? document.body.scrollHeight : 0, innerHeight) })`

// messageHandler is the name the diagnostics hooks post through: window.webkit.messageHandlers.<name>.
const messageHandler = "aicodingtool"

// diagnosticsScript reports the page's console and requests to the host. A popup or frame reports
// nothing: only the tab the panel holds is inspected.
const diagnosticsScript = `(() => {
  const channel = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.` + messageHandler + `;
  if (window.opener || window.top !== window || !channel || window.__aicodingtoolHooks) return;
  Object.defineProperty(window, '__aicodingtoolHooks', { value: true });
  const post = channel.postMessage.bind(channel);
  const send = (message) => { try { post(JSON.stringify(Object.assign({ o: location.origin }, message))); } catch (_) {} };
  const describe = (value) => {
    if (typeof value === 'string') return value;
    if (value instanceof Error) return String(value);
    if (value === undefined) return 'undefined';
    if (typeof value === 'function' || typeof value === 'symbol') return String(value);
    try { const json = JSON.stringify(value); return json === undefined ? String(value) : json; } catch (_) { return String(value); }
  };
  const format = (args) => {
    const list = Array.from(args);
    if (typeof list[0] === 'string' && /%[sdifoOc]/.test(list[0])) {
      let rest = list.slice(1);
      const head = list[0].replace(/%([sdifoOc])/g, (match, kind) => {
        if (!rest.length) return match;
        const value = rest.shift();
        if (kind === 'c') return '';
        if (kind === 'd' || kind === 'i') return String(parseInt(value, 10));
        if (kind === 'f') return String(parseFloat(value));
        return describe(value);
      });
      return [head, ...rest.map(describe)].join(' ');
    }
    return list.map(describe).join(' ');
  };
  const caller = () => {
    const lines = String(new Error().stack || '').split('\n').slice(2);
    for (const line of lines) {
      const match = /@?(\S+?):(\d+):\d+\)?$/.exec(line.trim());
      if (match && !match[1].startsWith('[native')) return { source: match[1].replace(/^.*?@/, ''), line: Number(match[2]) };
    }
    return {};
  };
  const levels = { debug: 'debug', log: 'info', info: 'info', warn: 'warning', error: 'error', trace: 'info', assert: 'error' };
  for (const [name, level] of Object.entries(levels)) {
    const original = console[name];
    if (typeof original !== 'function') continue;
    console[name] = function (...args) {
      if (name === 'assert') {
        if (args[0]) return original.apply(this, args);
        send(Object.assign({ k: 'console', level, message: 'Assertion failed: ' + format(args.slice(1)) }, caller()));
      } else {
        send(Object.assign({ k: 'console', level, message: format(args) }, caller()));
      }
      return original.apply(this, args);
    };
  }
  addEventListener('error', (event) => {
    if (event.target !== window && event.target && event.target.tagName) return;
    const message = event.error instanceof Error ? 'Uncaught ' + String(event.error) : 'Uncaught ' + (event.message || 'error');
    send({ k: 'console', level: 'error', message, source: event.filename || undefined, line: event.lineno || undefined });
  });
  addEventListener('unhandledrejection', (event) => {
    send({ k: 'console', level: 'error', message: 'Uncaught (in promise) ' + describe(event.reason) });
  });

  let next = 0;
  const begin = (method, url, type) => {
    const id = ++next;
    send({ k: 'net-start', id });
    return { id, method: String(method || 'GET').toUpperCase(), url: new URL(String(url), location.href).href, type, started: Date.now(), clock: performance.now() };
  };
  const finish = (request, result) => {
    send(Object.assign({ k: 'net', id: request.id, method: request.method, url: request.url, type: request.type, started: request.started, duration: Math.max(0, Math.round(performance.now() - request.clock)) }, result));
  };
  const nativeFetch = window.fetch;
  if (typeof nativeFetch === 'function') {
    window.fetch = function (input, init) {
      let request;
      try {
        const method = (init && init.method) || (input && typeof input === 'object' && 'method' in input ? input.method : 'GET');
        const url = input && typeof input === 'object' && 'url' in input ? input.url : input;
        request = begin(method, url, 'xhr');
      } catch (_) {}
      const pending = nativeFetch.apply(this, arguments);
      if (!request) return pending;
      return pending.then((response) => { finish(request, { status: response.status }); return response; },
        (error) => { finish(request, { error: String((error && error.message) || error) }); throw error; });
    };
  }
  const NativeXHR = window.XMLHttpRequest;
  if (typeof NativeXHR === 'function') {
    const open = NativeXHR.prototype.open;
    const sendRequest = NativeXHR.prototype.send;
    NativeXHR.prototype.open = function (method, url) {
      this.__aicodingtool = { method, url };
      return open.apply(this, arguments);
    };
    NativeXHR.prototype.send = function () {
      const opened = this.__aicodingtool;
      if (opened) {
        let request;
        try { request = begin(opened.method, opened.url, 'xhr'); } catch (_) {}
        if (request) {
          let done = false;
          const end = (result) => { if (!done) { done = true; finish(request, result); } };
          this.addEventListener('load', () => end({ status: this.status }));
          this.addEventListener('error', () => end({ error: 'net::ERR_FAILED' }));
          this.addEventListener('abort', () => end({ error: 'net::ERR_ABORTED' }));
          this.addEventListener('timeout', () => end({ error: 'net::ERR_TIMED_OUT' }));
        }
      }
      return sendRequest.apply(this, arguments);
    };
  }
  const kinds = { img: 'image', image: 'image', script: 'script', link: 'stylesheet', css: 'other', iframe: 'subFrame', frame: 'subFrame',
    video: 'media', audio: 'media', track: 'media', beacon: 'ping', object: 'object', embed: 'object', input: 'image', other: 'other' };
  const kindOf = (entry) => {
    if (entry.initiatorType === 'css') {
      if (/\.(woff2?|ttf|otf|eot)(\?|#|$)/i.test(entry.name)) return 'font';
      if (/\.(css)(\?|#|$)/i.test(entry.name)) return 'stylesheet';
      return 'image';
    }
    if (entry.initiatorType === 'link' && /\.(woff2?|ttf|otf|eot)(\?|#|$)/i.test(entry.name)) return 'font';
    return kinds[entry.initiatorType] || 'other';
  };
  const resources = (list) => {
    for (const entry of list.getEntries()) {
      if (entry.initiatorType === 'fetch' || entry.initiatorType === 'xmlhttprequest') continue;
      const message = { k: 'net', method: 'GET', url: entry.name, type: kindOf(entry), started: Math.round(performance.timeOrigin + entry.startTime), duration: Math.max(0, Math.round(entry.duration)) };
      if (entry.responseStatus) message.status = entry.responseStatus;
      if (entry.transferSize === 0 && entry.encodedBodySize > 0) message.cached = true;
      send(message);
    }
  };
  try { new PerformanceObserver(resources).observe({ type: 'resource', buffered: true }); } catch (_) {}
})();`
