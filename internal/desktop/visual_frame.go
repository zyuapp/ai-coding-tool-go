package desktop

// Inline script and style are the visual itself; everything that would leave the frame is refused.
const visualFrameCSP = "default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; img-src data: blob:; media-src data: blob:; font-src data:; form-action 'none'; base-uri 'none'"

// The one page the visual scheme serves: the empty frame a visual is posted into.
const visualFrameDocument = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><style>
*, *::before, *::after { box-sizing: border-box; }
html { background: transparent; overflow: hidden; scrollbar-width: thin; scrollbar-color: var(--color-border-strong) transparent; }
body {
  position: relative; display: flow-root; margin: 0;
  color: var(--color-text); font: var(--font-size, 14px)/1.5 var(--font-sans, system-ui, sans-serif);
  accent-color: var(--color-accent); -webkit-font-smoothing: antialiased;
}
button, input, select, textarea { font: inherit; color: inherit; }
</style><script>(() => {
  const host = window.parent;
  const post = (message) => host.postMessage(message, "*");
  let rendered = false;
  let reported = -1;
  let queued = false;
  let failed = false;
  const measure = () => {
    queued = false;
    const body = document.body;
    const height = Math.ceil(Math.max(body.scrollHeight, body.getBoundingClientRect().height));
    /**
     * A frame sized to its content never scrolls. A scrollbar would narrow the content, and content
     * that scales with its width would then fit only with the scrollbar, which keeps it there.
     */
    document.documentElement.style.overflowY = height > 2000 ? "auto" : "hidden";
    if (height === reported) return;
    reported = height;
    post({ type: "aicodingtool-visual:size", height });
  };
  const schedule = () => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(measure);
  };
  const style = (scheme, vars) => {
    const root = document.documentElement;
    if (vars && typeof vars === "object") {
      for (const [name, value] of Object.entries(vars)) {
        if (/^--[a-z0-9-]+$/.test(name) && typeof value === "string") root.style.setProperty(name, value);
      }
    }
    if (scheme === "light" || scheme === "dark") root.style.colorScheme = scheme;
  };
  const report = (message) => {
    if (failed) return;
    failed = true;
    post({ type: "aicodingtool-visual:error", message: String(message) });
  };
  /**
   * A ResizeObserver loop is the browser deferring a notification to the next frame, which the
   * visual still receives. Reporting it would flag a visual that draws fine and use up the one report.
   */
  addEventListener("error", (event) => {
    if (/^ResizeObserver loop/.test(event.message)) return;
    report(event.message || "Script error");
  });
  addEventListener("unhandledrejection", (event) => report(event.reason && event.reason.message || event.reason));
  addEventListener("message", (event) => {
    const data = event.data;
    if (event.source !== host || !data || typeof data !== "object") return;
    if (data.type === "aicodingtool-visual:theme") {
      style(data.scheme, data.vars);
      dispatchEvent(new Event("visualthemechange"));
      schedule();
    }
    if (data.type !== "aicodingtool-visual:render" || rendered || typeof data.html !== "string") return;
    rendered = true;
    style(data.scheme, data.vars);
    const template = document.createElement("template");
    template.innerHTML = data.html;
    /** Script parsed into a template never runs; a copy made here runs once it is in the page. */
    for (const inert of template.content.querySelectorAll("script")) {
      const live = document.createElement("script");
      for (const attribute of inert.attributes) live.setAttribute(attribute.name, attribute.value);
      live.textContent = inert.textContent;
      inert.replaceWith(live);
    }
    const body = document.body;
    new ResizeObserver(schedule).observe(body);
    new MutationObserver(schedule).observe(body, { subtree: true, childList: true, attributes: true, characterData: true });
    addEventListener("load", schedule, true);
    body.append(template.content);
    schedule();
  });
})();</script></head><body></body></html>`
