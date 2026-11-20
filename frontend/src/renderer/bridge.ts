import type { DesktopPlatform, ShortcutInvocation, WindowDesktopAPI, WindowDesktopCall, WindowMaterial } from "../contracts/ipc";
import type { WorkspaceBridge } from "../application/workspace-runtime";
import { keystrokeOf, resolveShortcuts, shortcutFor, formatShortcut, type ShortcutBinding, type ShortcutOverrides } from "../domain/shortcuts";

/**
 * Builds `window.desktop` and `window.workspace` on the Wails runtime, in the shapes the Electron
 * preload gave them, and supplies what a system web view lacks: the app's own schemes, the paths of
 * dropped files, the keyboard main matched before the page, and links that leave the app.
 */

type Ticket = { page: string; seq: number; absent: number[] };
type GoBridge = {
  Call(ticket: Ticket, name: string, args: unknown[]): Promise<unknown>;
  Request(ticket: Ticket, input: unknown): Promise<unknown>;
  Migrate(ticket: Ticket, values: unknown): Promise<void>;
  OpenExternal(url: string): Promise<void>;
  Loaded(): Promise<void>;
  TitlebarDoubleClick(): Promise<void>;
  LocalFonts(): Promise<string[]>;
};
type DesktopIdentity = { platform?: DesktopPlatform; windowMaterial?: WindowMaterial; reducedTransparency?: boolean };

declare global {
  interface Window {
    go?: { desktop?: { Bridge?: GoBridge } };
    __aicodingtoolDesktop?: DesktopIdentity;
    __aicodingtoolPush?: (channel: string, payload: unknown) => void;
  }
}

const go = (): GoBridge => {
  const bridge = window.go?.desktop?.Bridge;
  if (!bridge) throw new Error("The desktop bridge is not available.");
  return bridge;
};

/** Pushed messages, by channel. Main may push before anything listens; those are simply dropped, as Electron's are. */
const listeners = new Map<string, Set<(payload: unknown) => void>>();

function emit(channel: string, payload: unknown) {
  for (const listener of listeners.get(channel) ?? []) {
    try {
      listener(payload);
    } catch (error) {
      console.error(`A ${channel} listener failed:`, error);
    }
  }
}

window.__aicodingtoolPush = emit;

function pushed<Payload>(channel: string) {
  return (listener: (payload: Payload) => void) => {
    const handler = listener as (payload: unknown) => void;
    let set = listeners.get(channel);
    if (!set) listeners.set(channel, set = new Set());
    set.add(handler);
    return () => { set.delete(handler); };
  };
}

/** Wails runs each call on a goroutine of its own, so calls carry their order for Go to restore. */
const page = Math.random().toString(36).slice(2) + Date.now().toString(36);
let sequence = 0;

function ticket(args: readonly unknown[]): Ticket {
  const absent: number[] = [];
  args.forEach((value, index) => { if (value === undefined) absent.push(index); });
  return { page, seq: ++sequence, absent };
}

function relay<Name extends WindowDesktopCall>(name: Name) {
  return ((...args: unknown[]) => go().Call(ticket(args), name, args.map((value) => value === undefined ? null : value))) as WindowDesktopAPI[Name];
}

const identity = window.__aicodingtoolDesktop ?? {};
const platform: DesktopPlatform = identity.platform ?? (/Mac/.test(navigator.platform) ? "macos" : /Linux/.test(navigator.platform) ? "linux" : "other");
const windowMaterial: WindowMaterial = identity.windowMaterial ?? "none";

/** Main may say whether the window is in use before the page listens, so the last word is kept. */
let windowActive: boolean | undefined;
pushed<boolean>("window:active")((active) => { windowActive = active; });
const onWindowActive = pushed<boolean>("window:active");
let glassTint: number | undefined;
pushed<number>("window:glass-tint")((amount) => { glassTint = amount; });
const onGlassTint = pushed<number>("window:glass-tint");

/** Only macOS reports the native window's focus; elsewhere the page's own focus stands in for it. */
if (platform !== "macos") {
  window.addEventListener("focus", () => emit("window:active", true));
  window.addEventListener("blur", () => emit("window:active", false));
}

/**
 * A system web view gives a dropped file no path. The native drop reports the paths beside the
 * page's own drop, so the page's drop is held until they arrive and then delivered again with them.
 */
const droppedPaths = new WeakMap<File, string>();
type NativeDrop = { paths: string[]; at: number };
let nativeDrop: NativeDrop | null = null;
let awaitingDrop: ((drop: NativeDrop | null) => void) | null = null;
const redelivered = new WeakSet<Event>();
const DROP_WAIT_MS = 500;

pushed<{ paths: string[] }>("window:file-drop")(({ paths }) => {
  const drop = { paths, at: Date.now() };
  if (awaitingDrop) {
    awaitingDrop(drop);
    awaitingDrop = null;
  } else nativeDrop = drop;
});

function nativePaths(): Promise<NativeDrop | null> {
  if (nativeDrop && Date.now() - nativeDrop.at < DROP_WAIT_MS * 2) {
    const drop = nativeDrop;
    nativeDrop = null;
    return Promise.resolve(drop);
  }
  return new Promise((resolve) => {
    awaitingDrop = resolve;
    setTimeout(() => {
      if (awaitingDrop !== resolve) return;
      awaitingDrop = null;
      resolve(null);
    }, DROP_WAIT_MS);
  });
}

function baseName(path: string) {
  return path.slice(path.lastIndexOf("/") + 1);
}

window.addEventListener("drop", (event) => {
  if (redelivered.has(event) || !event.dataTransfer || !Array.from(event.dataTransfer.types).includes("Files")) return;
  const files = Array.from(event.dataTransfer.files);
  if (files.length === 0) return;
  event.preventDefault();
  event.stopImmediatePropagation();
  const target = event.target ?? window;
  const init: DragEventInit = {
    bubbles: true, cancelable: true, composed: true,
    clientX: event.clientX, clientY: event.clientY, screenX: event.screenX, screenY: event.screenY,
    altKey: event.altKey, ctrlKey: event.ctrlKey, metaKey: event.metaKey, shiftKey: event.shiftKey,
  };
  void nativePaths().then((drop) => {
    const remaining = [...(drop?.paths ?? [])];
    for (const file of files) {
      const index = remaining.findIndex((path) => baseName(path) === file.name);
      if (index >= 0) droppedPaths.set(file, remaining.splice(index, 1)[0]);
    }
    const transfer = new DataTransfer();
    for (const file of files) transfer.items.add(file);
    const again = new DragEvent("drop", { ...init, dataTransfer: transfer });
    redelivered.add(again);
    target.dispatchEvent(again);
  });
}, true);

/**
 * WebKit does not know `prefers-reduced-transparency`, so every rule behind it would never apply.
 * Each such media query is answered from the system setting main reports, and again when it changes.
 */
const TRANSPARENCY = /\(\s*prefers-reduced-transparency\s*(?::\s*(no-preference|reduce)\s*)?\)/g;
const transparencyQueries = new WeakMap<CSSMediaRule, string>();
let reducedTransparency = identity.reducedTransparency === true;

function answerTransparency(rules: CSSRuleList) {
  for (const rule of Array.from(rules)) {
    if (rule instanceof CSSMediaRule) {
      const query = transparencyQueries.get(rule) ?? (rule.conditionText.includes("prefers-reduced-transparency") ? rule.conditionText : null);
      if (query !== null) {
        transparencyQueries.set(rule, query);
        const answered = query.replace(TRANSPARENCY, (_feature, value: string | undefined) => (value === "no-preference") !== reducedTransparency ? "(min-width: 0px)" : "(max-width: -1px)");
        if (rule.media.mediaText !== answered) rule.media.mediaText = answered;
      }
    }
    if ("cssRules" in rule) answerTransparency((rule as CSSGroupingRule).cssRules);
  }
}

function answerAllTransparency() {
  for (const sheet of Array.from(document.styleSheets)) {
    try {
      answerTransparency(sheet.cssRules);
    } catch {
      // A sheet from another origin keeps its rules to itself.
    }
  }
}

const transparencyKnown = matchMedia("(prefers-reduced-transparency: no-preference)").matches || matchMedia("(prefers-reduced-transparency: reduce)").matches;
if (!transparencyKnown) {
  document.addEventListener("load", (event) => { if (event.target instanceof HTMLLinkElement) answerAllTransparency(); }, true);
  new MutationObserver(answerAllTransparency).observe(document.head, { childList: true });
  pushed<boolean>("window:reduced-transparency")((reduced) => {
    reducedTransparency = reduced;
    answerAllTransparency();
  });
  answerAllTransparency();
}

/** Electron served these schemes itself; here they are paths of the page's own origin. */
const SCHEMES: ReadonlyArray<[string, string]> = [
  ["attachment://file/", "/__app/attachment/"],
  ["message-image://file/?", "/__app/message-image?"],
  ["message-image://file/", "/__app/message-image"],
  ["aicodingtool-visual://frame/", "/__app/visual-frame"],
];

function servedUrl(value: string) {
  for (const [scheme, path] of SCHEMES) if (value.startsWith(scheme)) return path + value.slice(scheme.length);
  return value;
}

function rewriteProperty(prototype: object, property: string) {
  const descriptor = Object.getOwnPropertyDescriptor(prototype, property);
  if (!descriptor?.set || !descriptor.get) return;
  const { get, set } = descriptor;
  Object.defineProperty(prototype, property, {
    ...descriptor,
    get,
    set(this: Element, value: unknown) { set.call(this, typeof value === "string" ? servedUrl(value) : value); },
  });
}

rewriteProperty(HTMLImageElement.prototype, "src");
rewriteProperty(HTMLIFrameElement.prototype, "src");
rewriteProperty(HTMLSourceElement.prototype, "src");
const setAttribute = Element.prototype.setAttribute;
Element.prototype.setAttribute = function (this: Element, name: string, value: string) {
  return setAttribute.call(this, name, (name === "src" || name === "href") && typeof value === "string" ? servedUrl(value) : value);
};
const nativeFetch = window.fetch.bind(window);
window.fetch = (input: RequestInfo | URL, init?: RequestInit) => nativeFetch(typeof input === "string" ? servedUrl(input) : input, init);

/**
 * The keyboard main matched before the page saw a keystroke: an app shortcut is taken whole, and a
 * keystroke settings wait for is handed to them instead.
 */
let shortcuts: ShortcutBinding[] = resolveShortcuts({});
let capturing = false;
let talkKey: string | null = null;
const MODIFIER_KEYS = new Set(["Meta", "Control", "Alt", "Shift"]);
const mac = platform === "macos";

pushed<{ overrides: ShortcutOverrides; capturing: boolean }>("window:keyboard")((state) => {
  shortcuts = resolveShortcuts(state.overrides ?? {});
  capturing = state.capturing;
});

/** Main's messages arrive after the keystroke's own dispatch, so these do too. */
function later(channel: string, payload: unknown) {
  setTimeout(() => emit(channel, payload), 0);
}

function takeKey(event: KeyboardEvent) {
  event.preventDefault();
  event.stopImmediatePropagation();
}

window.addEventListener("keyup", (event) => {
  if (talkKey === null) return;
  if (event.code === talkKey || MODIFIER_KEYS.has(event.key)) {
    talkKey = null;
    later("window:shortcut", { action: "dictation.release", surface: "any" } satisfies ShortcutInvocation);
  }
}, true);

window.addEventListener("keydown", (event) => {
  if (event.isComposing) return;
  /** A fresh press of the held key means its release went to another app, so it is matched anew. */
  if (talkKey !== null && event.code === talkKey) {
    if (event.repeat) return;
    talkKey = null;
  }
  const stroke = keystrokeOf({ key: event.key, code: event.code, meta: event.metaKey, control: event.ctrlKey, alt: event.altKey, shift: event.shiftKey }, mac);
  if (!stroke) return;
  if (capturing) {
    if (stroke.key === "Escape") later("window:shortcut-captured", null);
    else if (stroke.mod || stroke.ctrl || stroke.alt) later("window:shortcut-captured", formatShortcut(stroke));
    else return;
    takeKey(event);
    return;
  }
  const binding = shortcutFor(shortcuts, stroke, "any");
  if (!binding) return;
  if (binding.action === "dictation.hold") {
    if (event.repeat) return;
    talkKey = event.code;
    later("window:shortcut", { action: binding.action, surface: "any" } satisfies ShortcutInvocation);
    return;
  }
  later("window:shortcut", { action: binding.action, surface: "any" } satisfies ShortcutInvocation);
  takeKey(event);
}, true);

/** A link the window follows outward only ever names a web page; the page itself never leaves. */
function openOutside(url: string) {
  void go().OpenExternal(url).catch(() => undefined);
}

window.open = ((url?: string | URL) => {
  if (url !== undefined) openOutside(String(url));
  return null;
}) as typeof window.open;

function followLink(event: MouseEvent) {
  if (event.defaultPrevented) return;
  const anchor = (event.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
  if (!anchor) return;
  const newWindow = anchor.target === "_blank" || event.button === 1 || event.metaKey || event.ctrlKey || event.shiftKey;
  if (!newWindow || !/^https?:/i.test(anchor.href)) return;
  event.preventDefault();
  openOutside(anchor.href);
}

window.addEventListener("click", followLink);
window.addEventListener("auxclick", (event) => { if (event.button === 1) followLink(event); });

/** A title bar answers a double-click as the system does, which the page's own drag region does not. */
window.addEventListener("dblclick", (event) => {
  const target = event.target as Element | null;
  if (!target || !(target instanceof Element)) return;
  if (getComputedStyle(target).getPropertyValue("--wails-draggable").trim() !== "drag") return;
  void go().TitlebarDoubleClick().catch(() => undefined);
});

/** Chromium's font access, answered from the fonts the system has installed. */
if (!("queryLocalFonts" in window)) {
  Object.defineProperty(window, "queryLocalFonts", {
    configurable: true,
    value: async () => (await go().LocalFonts()).map((family) => ({ family })),
  });
}

/** A custom scheme is not a secure context, which takes these away from the page. */
if (typeof crypto.randomUUID !== "function") {
  Object.defineProperty(crypto, "randomUUID", {
    configurable: true,
    value: () => {
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      bytes[6] = (bytes[6] & 0x0f) | 0x40;
      bytes[8] = (bytes[8] & 0x3f) | 0x80;
      const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
      return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
    },
  });
}

type WailsRuntime = { ClipboardSetText(text: string): Promise<boolean>; ClipboardGetText(): Promise<string> };

if (!navigator.clipboard) {
  const runtime = () => (window as unknown as { runtime: WailsRuntime }).runtime;
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: {
      async writeText(text: string) {
        if (!(await runtime().ClipboardSetText(text))) throw new Error("Could not copy.");
      },
      readText: () => runtime().ClipboardGetText(),
    },
  });
}

/** Main sends a frame's bytes as base64, which JSON can carry; the view decodes them as they arrived in Electron. */
type SimulatorFramePushed = Record<string, unknown> & { data: string };

function base64Bytes(value: string) {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

const desktop: WindowDesktopAPI = {
  platform,
  windowMaterial,
  setWindowGlassRegions: relay("setWindowGlassRegions"),
  pathForFile: (file: File) => droppedPaths.get(file) ?? "",
  openFolder: relay("openFolder"),
  projectlessWorkspace: relay("projectlessWorkspace"),
  commands: relay("commands"),
  branches: relay("branches"),
  commits: relay("commits"),
  diffPatch: relay("diffPatch"),
  describeFiles: relay("describeFiles"),
  saveAttachment: relay("saveAttachment"),
  readAttachment: relay("readAttachment"),
  readAttachmentContext: relay("readAttachmentContext"),
  setBrowserBounds: relay("setBrowserBounds"),
  focusBrowserTab: relay("focusBrowserTab"),
  terminalSnapshot: relay("terminalSnapshot"),
  readRemoteTerminal: relay("readRemoteTerminal"),
  setTheme: relay("setTheme"),
  watchSimulator: relay("watchSimulator"),
  unwatchSimulator: relay("unwatchSimulator"),
  onTerminalData: pushed("terminal:data"),
  onSimulatorFrame: (listener) => pushed<SimulatorFramePushed>("simulator:frame")((frame) => listener({ ...frame, data: base64Bytes(frame.data) } as Parameters<typeof listener>[0])),
  onWindowScreenshot: pushed("window:screenshot"),
  onDesktopShortcutRefused: pushed("window:shortcut-refused"),
  onShortcut: pushed("window:shortcut"),
  onShortcutCaptured: pushed("window:shortcut-captured"),
  onOpenThread: pushed("window:open-thread"),
  onDictationLevel: pushed("dictation:level"),
  onWindowActive: (listener) => {
    if (windowActive !== undefined) listener(windowActive);
    return onWindowActive(listener);
  },
  onGlassTint: (listener) => {
    if (glassTint !== undefined) listener(glassTint);
    return onGlassTint(listener);
  },
};

const workspace: WorkspaceBridge = {
  request: (input) => go().Request(ticket([input]), input ?? null) as ReturnType<WorkspaceBridge["request"]>,
  migrate: (values) => go().Migrate(ticket([values]), values),
  onUpdate: pushed("workspace-runtime:update"),
  onSurface: pushed("workspace-runtime:surface"),
};

Object.defineProperty(window, "desktop", { value: Object.freeze(desktop), configurable: false, writable: false });
Object.defineProperty(window, "workspace", { value: Object.freeze(workspace), configurable: false, writable: false });

/** Main repeats what it said before the page listened: whether the window is in use, its glass, its keyboard. */
void go().Loaded().catch(() => undefined);

export {};
