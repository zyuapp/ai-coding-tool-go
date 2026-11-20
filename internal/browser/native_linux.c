//go:build linux && cgo

#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
#include "native_linux.h"

extern int actBrowserDecide(uint64_t view, char *url, char *method, int target);
extern void actBrowserStarted(uint64_t view);
extern int actBrowserResponse(uint64_t view, char *url, int status, int mainFrame);
extern void actBrowserCommitted(uint64_t view);
extern void actBrowserFinished(uint64_t view);
extern void actBrowserFailed(uint64_t view, int provisional, char *description, char *message);
extern void actBrowserTerminated(uint64_t view);
extern void actBrowserChanged(uint64_t view, char *key, char *url, char *title, int loading, int back, int forward);
extern void actBrowserMessage(uint64_t view, char *origin, int fromURL, char *body);
extern void actBrowserPressed(uint64_t view);
extern int actBrowserKey(uint64_t view, char *type, char *key, char *code, int meta, int control, int alt, int shift, int repeat);
extern uint64_t actBrowserPopup(uint64_t opener, char *url);
extern void actBrowserPopupClosed(uint64_t view);
extern void actBrowserEvaluated(uint64_t call, char *json, char *error);
extern void actBrowserDrawn(uint64_t call, void *bytes, int length, char *error);
extern void actBrowserFound(uint64_t call, int found);
extern void actBrowserCookieList(uint64_t call, uint64_t snapshot, char *json);
extern void actBrowserDone(uint64_t call, int ok);

/* Must match frameTarget in engine.go. */
enum { TargetPage = 0, TargetFrame = 1, TargetUnknown = 2 };

#define HOST_WORLD "aicodingtool-host"
#define PARKED_WIDTH 1200
#define PARKED_HEIGHT 800
#define POPUP_WIDTH 560
#define POPUP_HEIGHT 720
#define MAX_SHOT_POINTS 8000

typedef struct {
  uint64_t handle;
  WebKitWebView *view;
  /* A popup's own window; NULL for a tab's page. */
  GtkWidget *window;
  WebKitUserContentManager *content;
  gboolean closed;
  guint16 lastDown;
} Page;

static GtkWidget *hostWindow;
static WebKitWebView *hostView;
static GtkWidget *overlay;
static GtkWidget *parking;
static GtkWidget *parked;
static GHashTable *pages;
static WebKitWebContext *context;
static WebKitWebsiteDataManager *manager;
static WebKitSettings *settings;
static char *diagnosticsSource;
static char *mouseSource;
static char *handlerName;
static uint64_t cookieSnapshotID;
static GList *cookieSnapshot;

/* Runs work on the GTK main thread and waits for it. */
typedef struct {
  void (*work)(void *);
  void *data;
  GMutex lock;
  GCond cond;
  gboolean done;
} SyncCall;

static gboolean syncTrampoline(gpointer data) {
  SyncCall *call = data;
  call->work(call->data);
  g_mutex_lock(&call->lock);
  call->done = TRUE;
  g_cond_signal(&call->cond);
  g_mutex_unlock(&call->lock);
  return G_SOURCE_REMOVE;
}

static void runOnMain(void (*work)(void *), void *data) {
  if (g_main_context_is_owner(g_main_context_default())) {
    work(data);
    return;
  }
  SyncCall call = {.work = work, .data = data};
  g_mutex_init(&call.lock);
  g_cond_init(&call.cond);
  g_idle_add_full(G_PRIORITY_HIGH_IDLE, syncTrampoline, &call, NULL);
  g_mutex_lock(&call.lock);
  while (!call.done) g_cond_wait(&call.cond, &call.lock);
  g_mutex_unlock(&call.lock);
  g_mutex_clear(&call.lock);
  g_cond_clear(&call.cond);
}

/* Runs work on the GTK main thread later; work owns and frees data. */
static void later(GSourceFunc work, void *data) { g_idle_add_full(G_PRIORITY_DEFAULT, work, data, NULL); }

static Page *pageFor(uint64_t handle) { return pages ? g_hash_table_lookup(pages, GSIZE_TO_POINTER(handle)) : NULL; }

static char *copied(const char *text) { return strdup(text ? text : ""); }

static void reportState(Page *page, const char *key) {
  if (page->closed) return;
  WebKitWebView *view = page->view;
  actBrowserChanged(page->handle, (char *)key, (char *)(webkit_web_view_get_uri(view) ?: ""), (char *)(webkit_web_view_get_title(view) ?: ""),
                    webkit_web_view_is_loading(view), webkit_web_view_can_go_back(view), webkit_web_view_can_go_forward(view));
}

static void onLoading(GObject *object, GParamSpec *spec, Page *page) { reportState(page, "loading"); }
static void onTitle(GObject *object, GParamSpec *spec, Page *page) { reportState(page, "title"); }
static void onURI(GObject *object, GParamSpec *spec, Page *page) { reportState(page, "URL"); }
static void onHistory(WebKitBackForwardList *list, WebKitBackForwardListItem *added, gpointer removed, Page *page) { reportState(page, "canGoBack"); }

/* Whether a navigation loads the top document. WebKitGTK says so from 2.56 on; earlier ones do not. */
static int navigationTarget(WebKitNavigationAction *action, WebKitNavigationPolicyDecision *decision) {
  typedef void *(*TargetFrameInfo)(WebKitNavigationAction *);
  typedef gboolean (*IsMainFrameInfo)(void *);
  static gboolean looked = FALSE;
  static TargetFrameInfo targetFrame;
  static IsMainFrameInfo isMainFrame;
  if (!looked) {
    looked = TRUE;
    targetFrame = (TargetFrameInfo)dlsym(RTLD_DEFAULT, "webkit_navigation_action_get_target_frame_info");
    isMainFrame = (IsMainFrameInfo)dlsym(RTLD_DEFAULT, "webkit_frame_info_is_main_frame");
  }
  if (targetFrame && isMainFrame) {
    void *info = targetFrame(action);
    if (info) return isMainFrame(info) ? TargetPage : TargetFrame;
  }
  G_GNUC_BEGIN_IGNORE_DEPRECATIONS
  const char *name = webkit_navigation_policy_decision_get_frame_name(decision);
  G_GNUC_END_IGNORE_DEPRECATIONS
  if (name && *name) return TargetFrame;
  return TargetUnknown;
}

static gboolean onDecide(WebKitWebView *view, WebKitPolicyDecision *decision, WebKitPolicyDecisionType type, Page *page) {
  if (page->closed) {
    webkit_policy_decision_ignore(decision);
    return TRUE;
  }
  if (type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
    /* The window it would open is answered by the create signal. */
    webkit_policy_decision_use(decision);
    return TRUE;
  }
  if (type == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION) {
    WebKitNavigationPolicyDecision *navigation = WEBKIT_NAVIGATION_POLICY_DECISION(decision);
    WebKitNavigationAction *action = webkit_navigation_policy_decision_get_navigation_action(navigation);
    WebKitURIRequest *request = webkit_navigation_action_get_request(action);
    const char *uri = webkit_uri_request_get_uri(request);
    const char *method = webkit_uri_request_get_http_method(request);
    int target = navigationTarget(action, navigation);
    if (target != TargetFrame && uri && (!strcmp(uri, "about:blank") || !strcmp(uri, "about:srcdoc"))) {
      webkit_policy_decision_use(decision);
      return TRUE;
    }
    if (actBrowserDecide(page->handle, (char *)(uri ?: ""), (char *)(method ?: "GET"), target)) webkit_policy_decision_use(decision);
    else webkit_policy_decision_ignore(decision);
    return TRUE;
  }
  if (type == WEBKIT_POLICY_DECISION_TYPE_RESPONSE) {
    WebKitResponsePolicyDecision *answer = WEBKIT_RESPONSE_POLICY_DECISION(decision);
    WebKitURIResponse *response = webkit_response_policy_decision_get_response(answer);
#if WEBKIT_CHECK_VERSION(2, 40, 0)
    gboolean main = webkit_response_policy_decision_is_main_frame_main_resource(answer);
#else
    gboolean main = FALSE;
#endif
    int allowed = actBrowserResponse(page->handle, (char *)(webkit_uri_response_get_uri(response) ?: ""), (int)webkit_uri_response_get_status_code(response), main);
    /* The panel shows pages; a file it cannot show is not downloaded behind the user's back. */
    if (allowed && webkit_response_policy_decision_is_mime_type_supported(answer)) webkit_policy_decision_use(decision);
    else webkit_policy_decision_ignore(decision);
    return TRUE;
  }
  return FALSE;
}

static void onLoadChanged(WebKitWebView *view, WebKitLoadEvent event, Page *page) {
  if (page->closed) return;
  switch (event) {
  case WEBKIT_LOAD_STARTED: actBrowserStarted(page->handle); break;
  case WEBKIT_LOAD_COMMITTED: actBrowserCommitted(page->handle); break;
  case WEBKIT_LOAD_FINISHED: actBrowserFinished(page->handle); break;
  default: break;
  }
}

static gboolean onLoadFailed(WebKitWebView *view, WebKitLoadEvent event, char *failing, GError *error, Page *page) {
  if (page->closed) return FALSE;
  /* A navigation cut short by another, or refused by the panel itself, is not a failure to show. */
  gboolean quiet = g_error_matches(error, WEBKIT_NETWORK_ERROR, WEBKIT_NETWORK_ERROR_CANCELLED) ||
                   g_error_matches(error, WEBKIT_POLICY_ERROR, WEBKIT_POLICY_ERROR_FRAME_LOAD_INTERRUPTED_BY_POLICY_CHANGE) ||
                   g_error_matches(error, WEBKIT_PLUGIN_ERROR, WEBKIT_PLUGIN_ERROR_WILL_HANDLE_LOAD);
  char *message = quiet ? g_strdup("") : (failing && *failing ? g_strdup_printf("%s (%s)", error->message, failing) : g_strdup(error->message));
  actBrowserFailed(page->handle, event == WEBKIT_LOAD_STARTED, error->message, message);
  g_free(message);
  return FALSE;
}

static void onTerminated(WebKitWebView *view, WebKitWebProcessTerminationReason reason, Page *page) {
  if (!page->closed) actBrowserTerminated(page->handle);
}

static void onMessage(WebKitUserContentManager *content, WebKitJavascriptResult *result, Page *page) {
  if (page->closed) return;
  JSCValue *value = webkit_javascript_result_get_js_value(result);
  if (!jsc_value_is_string(value)) return;
  char *body = jsc_value_to_string(value);
  actBrowserMessage(page->handle, (char *)(webkit_web_view_get_uri(page->view) ?: ""), 1, body);
  g_free(body);
}

static gboolean onPress(GtkWidget *widget, GdkEventButton *event, Page *page) {
  if (!page->closed && event->type == GDK_BUTTON_PRESS) actBrowserPressed(page->handle);
  return FALSE;
}

/* DOM codes by evdev key code, which GDK reports eight higher. */
static const char *codeOf(guint16 hardware) {
  static const char *const codes[] = {
      [1] = "Escape", [2] = "Digit1", [3] = "Digit2", [4] = "Digit3", [5] = "Digit4", [6] = "Digit5", [7] = "Digit6", [8] = "Digit7", [9] = "Digit8",
      [10] = "Digit9", [11] = "Digit0", [12] = "Minus", [13] = "Equal", [14] = "Backspace", [15] = "Tab", [16] = "KeyQ", [17] = "KeyW",
      [18] = "KeyE", [19] = "KeyR", [20] = "KeyT", [21] = "KeyY", [22] = "KeyU", [23] = "KeyI", [24] = "KeyO", [25] = "KeyP",
      [26] = "BracketLeft", [27] = "BracketRight", [28] = "Enter", [29] = "ControlLeft", [30] = "KeyA", [31] = "KeyS", [32] = "KeyD",
      [33] = "KeyF", [34] = "KeyG", [35] = "KeyH", [36] = "KeyJ", [37] = "KeyK", [38] = "KeyL", [39] = "Semicolon", [40] = "Quote",
      [41] = "Backquote", [42] = "ShiftLeft", [43] = "Backslash", [44] = "KeyZ", [45] = "KeyX", [46] = "KeyC", [47] = "KeyV", [48] = "KeyB",
      [49] = "KeyN", [50] = "KeyM", [51] = "Comma", [52] = "Period", [53] = "Slash", [54] = "ShiftRight", [55] = "NumpadMultiply",
      [56] = "AltLeft", [57] = "Space", [58] = "CapsLock", [59] = "F1", [60] = "F2", [61] = "F3", [62] = "F4", [63] = "F5", [64] = "F6",
      [65] = "F7", [66] = "F8", [67] = "F9", [68] = "F10", [69] = "NumLock", [70] = "ScrollLock", [71] = "Numpad7", [72] = "Numpad8",
      [73] = "Numpad9", [74] = "NumpadSubtract", [75] = "Numpad4", [76] = "Numpad5", [77] = "Numpad6", [78] = "NumpadAdd", [79] = "Numpad1",
      [80] = "Numpad2", [81] = "Numpad3", [82] = "Numpad0", [83] = "NumpadDecimal", [86] = "IntlBackslash", [87] = "F11", [88] = "F12",
      [96] = "NumpadEnter", [97] = "ControlRight", [98] = "NumpadDivide", [99] = "PrintScreen", [100] = "AltRight", [102] = "Home",
      [103] = "ArrowUp", [104] = "PageUp", [105] = "ArrowLeft", [106] = "ArrowRight", [107] = "End", [108] = "ArrowDown", [109] = "PageDown",
      [110] = "Insert", [111] = "Delete", [117] = "NumpadEqual", [119] = "Pause", [125] = "MetaLeft", [126] = "MetaRight", [127] = "ContextMenu",
      [183] = "F13", [184] = "F14", [185] = "F15", [186] = "F16", [187] = "F17", [188] = "F18", [189] = "F19", [190] = "F20", [191] = "F21",
      [192] = "F22", [193] = "F23", [194] = "F24",
  };
  guint16 evdev = hardware >= 8 ? hardware - 8 : 0;
  if (evdev < G_N_ELEMENTS(codes) && codes[evdev]) return codes[evdev];
  return "";
}

/* The DOM key a keyval names, written into buffer when it is a character. */
static const char *keyOf(guint keyval, char *buffer, size_t size) {
  switch (keyval) {
  case GDK_KEY_Return: case GDK_KEY_KP_Enter: case GDK_KEY_ISO_Enter: return "Enter";
  case GDK_KEY_Escape: return "Escape";
  case GDK_KEY_Tab: case GDK_KEY_ISO_Left_Tab: case GDK_KEY_KP_Tab: return "Tab";
  case GDK_KEY_BackSpace: return "Backspace";
  case GDK_KEY_Delete: case GDK_KEY_KP_Delete: return "Delete";
  case GDK_KEY_Insert: case GDK_KEY_KP_Insert: return "Insert";
  case GDK_KEY_Home: case GDK_KEY_KP_Home: return "Home";
  case GDK_KEY_End: case GDK_KEY_KP_End: return "End";
  case GDK_KEY_Page_Up: case GDK_KEY_KP_Page_Up: return "PageUp";
  case GDK_KEY_Page_Down: case GDK_KEY_KP_Page_Down: return "PageDown";
  case GDK_KEY_Left: case GDK_KEY_KP_Left: return "ArrowLeft";
  case GDK_KEY_Right: case GDK_KEY_KP_Right: return "ArrowRight";
  case GDK_KEY_Up: case GDK_KEY_KP_Up: return "ArrowUp";
  case GDK_KEY_Down: case GDK_KEY_KP_Down: return "ArrowDown";
  case GDK_KEY_Shift_L: case GDK_KEY_Shift_R: return "Shift";
  case GDK_KEY_Control_L: case GDK_KEY_Control_R: return "Control";
  case GDK_KEY_Alt_L: case GDK_KEY_Alt_R: return "Alt";
  case GDK_KEY_Meta_L: case GDK_KEY_Meta_R: case GDK_KEY_Super_L: case GDK_KEY_Super_R: return "Meta";
  case GDK_KEY_Caps_Lock: return "CapsLock";
  case GDK_KEY_Menu: return "ContextMenu";
  default: break;
  }
  if (keyval >= GDK_KEY_F1 && keyval <= GDK_KEY_F24) {
    snprintf(buffer, size, "F%u", keyval - GDK_KEY_F1 + 1);
    return buffer;
  }
  gunichar character = gdk_keyval_to_unicode(keyval);
  if (character) {
    int length = g_unichar_to_utf8(character, buffer);
    buffer[length] = 0;
    return buffer;
  }
  const char *name = gdk_keyval_name(keyval);
  return name ? name : "";
}

/* Hands a key in a page to the app first, as Electron's before-input-event does. TRUE when the app took it. */
static gboolean onKey(GtkWidget *widget, GdkEventKey *event, Page *page) {
  if (page->closed) return FALSE;
  gboolean down = event->type == GDK_KEY_PRESS;
  gboolean repeat = down && page->lastDown == event->hardware_keycode;
  page->lastDown = down ? event->hardware_keycode : 0;
  char buffer[16];
  const char *key = keyOf(event->keyval, buffer, sizeof buffer);
  GdkModifierType state = event->state;
  return actBrowserKey(page->handle, down ? "keyDown" : "keyUp", (char *)key, (char *)codeOf(event->hardware_keycode),
                       (state & (GDK_META_MASK | GDK_SUPER_MASK)) != 0, (state & GDK_CONTROL_MASK) != 0, (state & GDK_MOD1_MASK) != 0,
                       (state & GDK_SHIFT_MASK) != 0, repeat) != 0;
}

/* Nothing in the panel would ask the user, so a page gets only what is harmless without asking. */
static gboolean onPermission(WebKitWebView *view, WebKitPermissionRequest *request, Page *page) {
  const char *type = G_OBJECT_TYPE_NAME(request);
  if (!strcmp(type, "WebKitPointerLockPermissionRequest") || !strcmp(type, "WebKitWebsiteDataAccessPermissionRequest")) webkit_permission_request_allow(request);
  else webkit_permission_request_deny(request);
  return TRUE;
}

static gboolean noContextMenu(WebKitWebView *view, WebKitContextMenu *menu, GdkEvent *event, WebKitHitTestResult *hit, Page *page) { return TRUE; }

static GtkWidget *onCreate(WebKitWebView *view, WebKitNavigationAction *action, Page *page);

static void watch(Page *page) {
  WebKitWebView *view = page->view;
  g_signal_connect(view, "decide-policy", G_CALLBACK(onDecide), page);
  g_signal_connect(view, "load-changed", G_CALLBACK(onLoadChanged), page);
  g_signal_connect(view, "load-failed", G_CALLBACK(onLoadFailed), page);
  g_signal_connect(view, "web-process-terminated", G_CALLBACK(onTerminated), page);
  g_signal_connect(view, "button-press-event", G_CALLBACK(onPress), page);
  g_signal_connect(view, "key-press-event", G_CALLBACK(onKey), page);
  g_signal_connect(view, "key-release-event", G_CALLBACK(onKey), page);
  g_signal_connect(view, "permission-request", G_CALLBACK(onPermission), page);
  g_signal_connect(view, "create", G_CALLBACK(onCreate), page);
}

static void forget(Page *page) {
  page->closed = TRUE;
  g_signal_handlers_disconnect_by_data(page->view, page);
  g_signal_handlers_disconnect_by_data(webkit_web_view_get_back_forward_list(page->view), page);
  if (page->content) {
    g_signal_handlers_disconnect_by_data(page->content, page);
    webkit_user_content_manager_unregister_script_message_handler(page->content, handlerName);
  }
  g_hash_table_remove(pages, GSIZE_TO_POINTER(page->handle));
}

static void freePage(Page *page) {
  if (page->content) g_object_unref(page->content);
  g_object_unref(page->view);
  g_free(page);
}

static void onPopupDestroyed(GtkWidget *window, Page *page) {
  if (page->closed) return;
  uint64_t handle = page->handle;
  forget(page);
  freePage(page);
  actBrowserPopupClosed(handle);
}

static void onPopupReady(WebKitWebView *view, Page *page) {
  if (!page->closed) gtk_widget_show_all(page->window);
}

static void onPopupClose(WebKitWebView *view, Page *page) {
  if (!page->closed) gtk_widget_destroy(page->window);
}

/* A page opening a window: a sign-in popup, usually. It shares the opener's session and checks. */
static GtkWidget *onCreate(WebKitWebView *view, WebKitNavigationAction *action, Page *page) {
  if (page->closed) return NULL;
  WebKitURIRequest *request = webkit_navigation_action_get_request(action);
  uint64_t handle = actBrowserPopup(page->handle, (char *)(webkit_uri_request_get_uri(request) ?: ""));
  if (!handle) return NULL;
  Page *popup = g_new0(Page, 1);
  popup->handle = handle;
  popup->view = WEBKIT_WEB_VIEW(webkit_web_view_new_with_related_view(view));
  g_object_ref_sink(popup->view);
  popup->window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
  gtk_window_set_default_size(GTK_WINDOW(popup->window), POPUP_WIDTH, POPUP_HEIGHT);
  gtk_window_set_position(GTK_WINDOW(popup->window), GTK_WIN_POS_CENTER);
  gtk_container_add(GTK_CONTAINER(popup->window), GTK_WIDGET(popup->view));
  g_hash_table_insert(pages, GSIZE_TO_POINTER(handle), popup);
  watch(popup);
  g_signal_connect(popup->view, "ready-to-show", G_CALLBACK(onPopupReady), popup);
  g_signal_connect(popup->view, "close", G_CALLBACK(onPopupClose), popup);
  g_signal_connect(popup->window, "destroy", G_CALLBACK(onPopupDestroyed), popup);
  return GTK_WIDGET(popup->view);
}

static WebKitWebView *findWebView(GtkWidget *widget) {
  if (WEBKIT_IS_WEB_VIEW(widget)) return WEBKIT_WEB_VIEW(widget);
  if (!GTK_IS_CONTAINER(widget)) return NULL;
  GList *children = gtk_container_get_children(GTK_CONTAINER(widget));
  WebKitWebView *found = NULL;
  for (GList *child = children; child && !found; child = child->next) found = findWebView(child->data);
  g_list_free(children);
  return found;
}

typedef struct {
  const char *sessionDir;
  char *failure;
} StartWork;

/* Puts an overlay where the app's own page is, so pages can be placed over it. */
static void startOnMain(void *data) {
  StartWork *work = data;
  if (!hostView) {
    GList *windows = gtk_window_list_toplevels();
    for (GList *item = windows; item && !hostView; item = item->next) {
      WebKitWebView *found = findWebView(item->data);
      if (found) {
        hostWindow = item->data;
        hostView = found;
      }
    }
    g_list_free(windows);
    if (!hostView) {
      work->failure = strdup("The browser panel has no window to draw pages over.");
      return;
    }
    GtkWidget *app = GTK_WIDGET(hostView);
    GtkWidget *box = gtk_widget_get_parent(app);
    overlay = gtk_overlay_new();
    g_object_ref(app);
    gtk_container_remove(GTK_CONTAINER(box), app);
    gtk_container_add(GTK_CONTAINER(overlay), app);
    g_object_unref(app);
    if (GTK_IS_BOX(box)) gtk_box_pack_start(GTK_BOX(box), overlay, TRUE, TRUE, 0);
    else gtk_container_add(GTK_CONTAINER(box), overlay);
    gtk_widget_show(overlay);
  }
  if (!pages) pages = g_hash_table_new(g_direct_hash, g_direct_equal);
  if (!context) {
    char *data = g_build_filename(work->sessionDir, "data", NULL);
    char *cache = g_build_filename(work->sessionDir, "cache", NULL);
    char *cookies = g_build_filename(work->sessionDir, "cookies.sqlite", NULL);
    g_mkdir_with_parents(work->sessionDir, 0700);
    manager = webkit_website_data_manager_new("base-data-directory", data, "base-cache-directory", cache, NULL);
    webkit_cookie_manager_set_persistent_storage(webkit_website_data_manager_get_cookie_manager(manager), cookies, WEBKIT_COOKIE_PERSISTENT_STORAGE_SQLITE);
    context = webkit_web_context_new_with_website_data_manager(manager);
    g_free(data);
    g_free(cache);
    g_free(cookies);
    settings = webkit_settings_new();
    webkit_settings_set_enable_developer_extras(settings, TRUE);
    webkit_settings_set_javascript_can_open_windows_automatically(settings, TRUE);
  }
  if (!parking) {
    /* Where a page waits while the panel is not showing it: a window drawn off screen, which gives
       it a viewport to lay out in without putting anything on screen. */
    parking = gtk_offscreen_window_new();
    parked = gtk_fixed_new();
    gtk_container_add(GTK_CONTAINER(parking), parked);
    gtk_window_set_default_size(GTK_WINDOW(parking), PARKED_WIDTH, PARKED_HEIGHT);
    gtk_widget_show_all(parking);
  }
}

char *actBrowserStart(const char *sessionDir, const char *diagnostics, const char *mouseNavigation, const char *handler) {
  g_free(diagnosticsSource);
  g_free(mouseSource);
  g_free(handlerName);
  diagnosticsSource = g_strdup(diagnostics);
  mouseSource = g_strdup(mouseNavigation);
  handlerName = g_strdup(handler);
  StartWork work = {sessionDir, NULL};
  runOnMain(startOnMain, &work);
  return work.failure;
}

static void giveKeysBack(GtkWidget *view) {
  if (hostView && (gtk_widget_has_focus(view) || gtk_widget_is_focus(view))) gtk_widget_grab_focus(GTK_WIDGET(hostView));
}

static void closePage(Page *page) {
  GtkWidget *widget = GTK_WIDGET(page->view);
  forget(page);
  if (page->window) {
    gtk_widget_destroy(page->window);
  } else {
    giveKeysBack(widget);
    webkit_web_view_stop_loading(page->view);
    GtkWidget *parent = gtk_widget_get_parent(widget);
    if (parent) gtk_container_remove(GTK_CONTAINER(parent), widget);
  }
  freePage(page);
}

static void stopOnMain(void *data) {
  if (!pages) return;
  GList *all = g_hash_table_get_values(pages);
  for (GList *item = all; item; item = item->next) closePage(item->data);
  g_list_free(all);
  g_list_free_full(cookieSnapshot, (GDestroyNotify)webkit_website_data_unref);
  cookieSnapshot = NULL;
}

void actBrowserStop(void) { runOnMain(stopOnMain, NULL); }

typedef struct {
  uint64_t handle;
  double x, y, width, height;
  int shown;
  const char *text;
  char *failure;
} ViewWork;

static void openOnMain(void *data) {
  ViewWork *work = data;
  if (!hostView) {
    work->failure = strdup("The browser panel has no window to draw pages over.");
    return;
  }
  WebKitUserContentManager *content = webkit_user_content_manager_new();
  WebKitUserScript *hooks = webkit_user_script_new(diagnosticsSource, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME, WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, NULL, NULL);
  WebKitUserScript *mouse = webkit_user_script_new(mouseSource, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME, WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, NULL, NULL);
  webkit_user_content_manager_add_script(content, hooks);
  webkit_user_content_manager_add_script(content, mouse);
  webkit_user_script_unref(hooks);
  webkit_user_script_unref(mouse);
  Page *page = g_new0(Page, 1);
  page->handle = work->handle;
  page->content = content;
  page->view = WEBKIT_WEB_VIEW(g_object_new(WEBKIT_TYPE_WEB_VIEW, "web-context", context, "user-content-manager", content, "settings", settings, NULL));
  g_object_ref_sink(page->view);
  char *signal = g_strdup_printf("script-message-received::%s", handlerName);
  g_signal_connect(content, signal, G_CALLBACK(onMessage), page);
  g_free(signal);
  webkit_user_content_manager_register_script_message_handler(content, handlerName);
  watch(page);
  g_signal_connect(page->view, "context-menu", G_CALLBACK(noContextMenu), page);
  g_signal_connect(page->view, "notify::is-loading", G_CALLBACK(onLoading), page);
  g_signal_connect(page->view, "notify::title", G_CALLBACK(onTitle), page);
  g_signal_connect(page->view, "notify::uri", G_CALLBACK(onURI), page);
  g_signal_connect(webkit_web_view_get_back_forward_list(page->view), "changed", G_CALLBACK(onHistory), page);
  g_hash_table_insert(pages, GSIZE_TO_POINTER(work->handle), page);
  gtk_widget_set_size_request(GTK_WIDGET(page->view), (int)work->width, (int)work->height);
  gtk_fixed_put(GTK_FIXED(parked), GTK_WIDGET(page->view), 0, 0);
  gtk_widget_show(GTK_WIDGET(page->view));
}

char *actBrowserOpen(uint64_t handle, double width, double height) {
  ViewWork work = {.handle = handle, .width = width, .height = height};
  runOnMain(openOnMain, &work);
  return work.failure;
}

static void loadOnMain(void *data) {
  ViewWork *work = data;
  Page *page = pageFor(work->handle);
  if (page) webkit_web_view_load_uri(page->view, work->text);
}

char *actBrowserLoad(uint64_t handle, const char *url) {
  ViewWork work = {.handle = handle, .text = url};
  runOnMain(loadOnMain, &work);
  return NULL;
}

static void backOnMain(void *data) {
  Page *page = pageFor(*(uint64_t *)data);
  if (page) webkit_web_view_go_back(page->view);
}

static void forwardOnMain(void *data) {
  Page *page = pageFor(*(uint64_t *)data);
  if (page) webkit_web_view_go_forward(page->view);
}

static void reloadOnMain(void *data) {
  Page *page = pageFor(*(uint64_t *)data);
  if (page) webkit_web_view_reload(page->view);
}

static void closeOnMain(void *data) {
  Page *page = pageFor(*(uint64_t *)data);
  if (page) closePage(page);
}

static void focusOnMain(void *data) {
  Page *page = pageFor(*(uint64_t *)data);
  if (page) gtk_widget_grab_focus(GTK_WIDGET(page->view));
}

void actBrowserBack(uint64_t handle) { runOnMain(backOnMain, &handle); }
void actBrowserForward(uint64_t handle) { runOnMain(forwardOnMain, &handle); }
void actBrowserReload(uint64_t handle) { runOnMain(reloadOnMain, &handle); }
void actBrowserClose(uint64_t handle) { runOnMain(closeOnMain, &handle); }
void actBrowserFocus(uint64_t handle) { runOnMain(focusOnMain, &handle); }

static void placeOnMain(void *data) {
  ViewWork *work = data;
  Page *page = pageFor(work->handle);
  if (!page || page->window || !overlay) return;
  GtkWidget *view = GTK_WIDGET(page->view);
  GtkWidget *home = work->shown ? overlay : parked;
  GtkWidget *parent = gtk_widget_get_parent(view);
  if (parent != home) {
    giveKeysBack(view);
    g_object_ref(view);
    if (parent) gtk_container_remove(GTK_CONTAINER(parent), view);
    if (work->shown) {
      gtk_widget_set_halign(view, GTK_ALIGN_START);
      gtk_widget_set_valign(view, GTK_ALIGN_START);
      gtk_overlay_add_overlay(GTK_OVERLAY(overlay), view);
    } else {
      gtk_widget_set_margin_start(view, 0);
      gtk_widget_set_margin_top(view, 0);
      gtk_fixed_put(GTK_FIXED(parked), view, 0, 0);
    }
    g_object_unref(view);
  }
  if (work->shown) {
    gtk_widget_set_margin_start(view, (int)work->x);
    gtk_widget_set_margin_top(view, (int)work->y);
  }
  gtk_widget_set_size_request(view, (int)work->width, (int)work->height);
  gtk_widget_show(view);
}

void actBrowserPlace(uint64_t handle, int shown, double x, double y, double width, double height) {
  ViewWork work = {.handle = handle, .shown = shown, .x = x, .y = y, .width = width, .height = height};
  runOnMain(placeOnMain, &work);
}

typedef struct {
  double zoom;
} ZoomWork;

static void zoomOnMain(void *data) {
  ZoomWork *work = data;
  if (hostView) work->zoom = webkit_web_view_get_zoom_level(hostView);
}

double actBrowserZoom(void) {
  ZoomWork work = {1};
  runOnMain(zoomOnMain, &work);
  return work.zoom > 0 ? work.zoom : 1;
}

typedef struct {
  uint64_t handle;
  ACTBrowserState *out;
} StateWork;

static void stateOnMain(void *data) {
  StateWork *work = data;
  Page *page = pageFor(work->handle);
  work->out->url = copied(page ? webkit_web_view_get_uri(page->view) : NULL);
  work->out->title = copied(page ? webkit_web_view_get_title(page->view) : NULL);
  work->out->loading = page && webkit_web_view_is_loading(page->view);
  work->out->canGoBack = page && webkit_web_view_can_go_back(page->view);
  work->out->canGoForward = page && webkit_web_view_can_go_forward(page->view);
}

void actBrowserState(uint64_t handle, ACTBrowserState *out) {
  StateWork work = {handle, out};
  runOnMain(stateOnMain, &work);
}

/* Asynchronous work: what each needs once it reaches the main thread. */
typedef struct {
  uint64_t handle;
  uint64_t call;
  char *text;
  int flag;
  double width, height;
  uint64_t snapshot;
  /* Find's signal handlers, disconnected once one answers. */
  WebKitFindController *finder;
  gulong found, failed;
} AsyncWork;

static void freeWork(AsyncWork *work) {
  g_free(work->text);
  g_free(work);
}

/* What a script threw, as the page wrote it, without the `Error: ` WebKit puts before it. */
static char *scriptError(GError *error) {
  const char *message = error->message;
  const char *inner = g_strrstr(message, ": Error: ");
  if (inner) message = inner + strlen(": Error: ");
  else if (g_str_has_prefix(message, "Error: ")) message += strlen("Error: ");
  return g_strdup(message);
}

static void answerValue(uint64_t call, JSCValue *value) {
  if (!value || jsc_value_is_null(value) || jsc_value_is_undefined(value)) {
    actBrowserEvaluated(call, "null", NULL);
    return;
  }
  char *json = jsc_value_to_string(value);
  actBrowserEvaluated(call, json, NULL);
  g_free(json);
}

#if WEBKIT_CHECK_VERSION(2, 40, 0)
static void evaluated(GObject *source, GAsyncResult *result, gpointer data) {
  AsyncWork *work = data;
  GError *error = NULL;
  JSCValue *value = webkit_web_view_call_async_javascript_function_finish(WEBKIT_WEB_VIEW(source), result, &error);
  if (error) {
    char *message = scriptError(error);
    actBrowserEvaluated(work->call, NULL, message);
    g_free(message);
    g_error_free(error);
  } else {
    answerValue(work->call, value);
  }
  if (value) g_object_unref(value);
  freeWork(work);
}
#else
static void evaluated(GObject *source, GAsyncResult *result, gpointer data) {
  AsyncWork *work = data;
  GError *error = NULL;
  WebKitJavascriptResult *answer = webkit_web_view_run_javascript_in_world_finish(WEBKIT_WEB_VIEW(source), result, &error);
  if (error) {
    char *message = scriptError(error);
    actBrowserEvaluated(work->call, NULL, message);
    g_free(message);
    g_error_free(error);
  } else {
    answerValue(work->call, webkit_javascript_result_get_js_value(answer));
  }
  if (answer) webkit_javascript_result_unref(answer);
  freeWork(work);
}
#endif

static gboolean evaluateOnMain(gpointer data) {
  AsyncWork *work = data;
  Page *page = pageFor(work->handle);
  if (!page) {
    actBrowserEvaluated(work->call, NULL, "The page went away while it was being read.");
    freeWork(work);
    return G_SOURCE_REMOVE;
  }
#if WEBKIT_CHECK_VERSION(2, 40, 0)
  char *body = g_strdup_printf("const value = await (%s);\nreturn value === undefined ? null : JSON.stringify(value);", work->text);
  webkit_web_view_call_async_javascript_function(page->view, body, -1, NULL, HOST_WORLD, NULL, NULL, evaluated, work);
#else
  char *body = g_strdup_printf("(() => { const value = (%s); return value === undefined ? null : JSON.stringify(value); })()", work->text);
  webkit_web_view_run_javascript_in_world(page->view, body, HOST_WORLD, NULL, evaluated, work);
#endif
  g_free(body);
  return G_SOURCE_REMOVE;
}

void actBrowserEvaluate(uint64_t handle, const char *script, uint64_t call) {
  AsyncWork *work = g_new0(AsyncWork, 1);
  work->handle = handle;
  work->call = call;
  work->text = g_strdup(script);
  later(evaluateOnMain, work);
}

static cairo_status_t appendBytes(void *closure, const unsigned char *data, unsigned int length) {
  g_byte_array_append(closure, data, length);
  return CAIRO_STATUS_SUCCESS;
}

static void drawn(GObject *source, GAsyncResult *result, gpointer data) {
  AsyncWork *work = data;
  GError *error = NULL;
  cairo_surface_t *surface = webkit_web_view_get_snapshot_finish(WEBKIT_WEB_VIEW(source), result, &error);
  if (!surface) {
    actBrowserDrawn(work->call, NULL, 0, error ? error->message : "The page drew nothing.");
    if (error) g_error_free(error);
    freeWork(work);
    return;
  }
  int width = cairo_image_surface_get_width(surface), height = cairo_image_surface_get_height(surface);
  int scale = gtk_widget_get_scale_factor(GTK_WIDGET(source));
  /* A long page is cut off rather than refused. */
  int limit = MAX_SHOT_POINTS * (scale > 0 ? scale : 1);
  cairo_surface_t *image = surface;
  if (height > limit) {
    image = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, width > 0 ? width : 1, limit);
    cairo_t *cut = cairo_create(image);
    cairo_set_source_surface(cut, surface, 0, 0);
    cairo_paint(cut);
    cairo_destroy(cut);
  }
  GByteArray *bytes = g_byte_array_new();
  cairo_status_t status = cairo_surface_write_to_png_stream(image, appendBytes, bytes);
  if (status == CAIRO_STATUS_SUCCESS) actBrowserDrawn(work->call, bytes->data, (int)bytes->len, NULL);
  else actBrowserDrawn(work->call, NULL, 0, (char *)cairo_status_to_string(status));
  g_byte_array_unref(bytes);
  if (image != surface) cairo_surface_destroy(image);
  cairo_surface_destroy(surface);
  freeWork(work);
}

static gboolean drawOnMain(gpointer data) {
  AsyncWork *work = data;
  Page *page = pageFor(work->handle);
  if (!page) {
    actBrowserDrawn(work->call, NULL, 0, "The page went away while it was being drawn.");
    freeWork(work);
    return G_SOURCE_REMOVE;
  }
  WebKitSnapshotRegion region = work->flag ? WEBKIT_SNAPSHOT_REGION_FULL_DOCUMENT : WEBKIT_SNAPSHOT_REGION_VISIBLE;
  webkit_web_view_get_snapshot(page->view, region, WEBKIT_SNAPSHOT_OPTIONS_NONE, NULL, drawn, work);
  return G_SOURCE_REMOVE;
}

void actBrowserDraw(uint64_t handle, int fullPage, double width, double height, uint64_t call) {
  AsyncWork *work = g_new0(AsyncWork, 1);
  work->handle = handle;
  work->call = call;
  work->flag = fullPage;
  work->width = width;
  work->height = height;
  later(drawOnMain, work);
}

static void findAnswered(AsyncWork *work, int found) {
  g_signal_handler_disconnect(work->finder, work->found);
  g_signal_handler_disconnect(work->finder, work->failed);
  g_object_unref(work->finder);
  actBrowserFound(work->call, found);
  freeWork(work);
}

static void onFound(WebKitFindController *finder, guint matches, AsyncWork *work) { findAnswered(work, 1); }
static void onNotFound(WebKitFindController *finder, AsyncWork *work) { findAnswered(work, 0); }

static gboolean findOnMain(gpointer data) {
  AsyncWork *work = data;
  Page *page = pageFor(work->handle);
  if (!page) {
    actBrowserFound(work->call, 0);
    freeWork(work);
    return G_SOURCE_REMOVE;
  }
  WebKitFindController *finder = webkit_web_view_get_find_controller(page->view);
  work->finder = g_object_ref(finder);
  work->found = g_signal_connect(finder, "found-text", G_CALLBACK(onFound), work);
  work->failed = g_signal_connect(finder, "failed-to-find-text", G_CALLBACK(onNotFound), work);
  const char *current = webkit_find_controller_get_search_text(finder);
  if (current && !strcmp(current, work->text)) {
    if (work->flag) webkit_find_controller_search_previous(finder);
    else webkit_find_controller_search_next(finder);
  } else {
    guint options = WEBKIT_FIND_OPTIONS_CASE_INSENSITIVE | WEBKIT_FIND_OPTIONS_WRAP_AROUND | (work->flag ? WEBKIT_FIND_OPTIONS_BACKWARDS : 0);
    webkit_find_controller_search(finder, work->text, options, G_MAXUINT);
  }
  return G_SOURCE_REMOVE;
}

void actBrowserFind(uint64_t handle, const char *query, int backwards, uint64_t call) {
  AsyncWork *work = g_new0(AsyncWork, 1);
  work->handle = handle;
  work->call = call;
  work->text = g_strdup(query);
  work->flag = backwards;
  later(findOnMain, work);
}

static void fetched(GObject *source, GAsyncResult *result, gpointer data) {
  AsyncWork *work = data;
  GList *records = webkit_website_data_manager_fetch_finish(manager, result, NULL);
  g_list_free_full(cookieSnapshot, (GDestroyNotify)webkit_website_data_unref);
  cookieSnapshot = records;
  uint64_t snapshot = ++cookieSnapshotID;
  /* One record per site, its name on a line of its own. */
  GString *names = g_string_new("");
  for (GList *item = records; item; item = item->next) {
    const char *name = webkit_website_data_get_name(item->data);
    g_string_append_printf(names, "%s%s", item == records ? "" : "\n", name ? name : "");
  }
  actBrowserCookieList(work->call, snapshot, names->str);
  g_string_free(names, TRUE);
  freeWork(work);
}

static gboolean cookiesOnMain(gpointer data) {
  AsyncWork *work = data;
  if (!manager) {
    actBrowserCookieList(work->call, 0, "[]");
    freeWork(work);
    return G_SOURCE_REMOVE;
  }
  webkit_website_data_manager_fetch(manager, WEBKIT_WEBSITE_DATA_COOKIES, NULL, fetched, work);
  return G_SOURCE_REMOVE;
}

void actBrowserCookies(uint64_t call) {
  AsyncWork *work = g_new0(AsyncWork, 1);
  work->call = call;
  later(cookiesOnMain, work);
}

typedef struct {
  uint64_t call;
  GList *records;
} RemoveWork;

static void removed(GObject *source, GAsyncResult *result, gpointer data) {
  RemoveWork *work = data;
  gboolean ok = webkit_website_data_manager_remove_finish(manager, result, NULL);
  actBrowserDone(work->call, ok);
  g_list_free_full(work->records, (GDestroyNotify)webkit_website_data_unref);
  g_free(work);
}

static gboolean deleteOnMain(gpointer data) {
  AsyncWork *work = data;
  GList *item = work->snapshot == cookieSnapshotID && work->flag >= 0 ? g_list_nth(cookieSnapshot, (guint)work->flag) : NULL;
  if (!manager || !item) {
    actBrowserDone(work->call, 0);
    freeWork(work);
    return G_SOURCE_REMOVE;
  }
  RemoveWork *removal = g_new0(RemoveWork, 1);
  removal->call = work->call;
  removal->records = g_list_append(NULL, webkit_website_data_ref(item->data));
  freeWork(work);
  webkit_website_data_manager_remove(manager, WEBKIT_WEBSITE_DATA_COOKIES, removal->records, NULL, removed, removal);
  return G_SOURCE_REMOVE;
}

void actBrowserDeleteCookie(uint64_t snapshot, int index, uint64_t call) {
  AsyncWork *work = g_new0(AsyncWork, 1);
  work->call = call;
  work->flag = index;
  work->snapshot = snapshot;
  later(deleteOnMain, work);
}

typedef struct {
  uint64_t call;
  SoupCookie *cookie;
} CookieWork;

static void added(GObject *source, GAsyncResult *result, gpointer data) {
  CookieWork *work = data;
  gboolean ok = webkit_cookie_manager_add_cookie_finish(WEBKIT_COOKIE_MANAGER(source), result, NULL);
  actBrowserDone(work->call, ok);
  soup_cookie_free(work->cookie);
  g_free(work);
}

static gboolean setCookieOnMain(gpointer data) {
  CookieWork *work = data;
  if (!manager) {
    actBrowserDone(work->call, 0);
    soup_cookie_free(work->cookie);
    g_free(work);
    return G_SOURCE_REMOVE;
  }
  webkit_cookie_manager_add_cookie(webkit_website_data_manager_get_cookie_manager(manager), work->cookie, NULL, added, work);
  return G_SOURCE_REMOVE;
}

/* sameSite: 0 unspecified, 1 none, 2 lax, 3 strict. expires is Unix seconds, negative for a session cookie. */
void actBrowserSetCookie(const char *name, const char *value, const char *domain, const char *path, int secure, int httpOnly, int sameSite, double expires, uint64_t call) {
  SoupCookie *cookie = soup_cookie_new(name, value, domain, path, -1);
  soup_cookie_set_secure(cookie, secure);
  soup_cookie_set_http_only(cookie, httpOnly);
#if SOUP_CHECK_VERSION(2, 70, 0)
  if (sameSite == 1) soup_cookie_set_same_site_policy(cookie, SOUP_SAME_SITE_POLICY_NONE);
  if (sameSite == 2) soup_cookie_set_same_site_policy(cookie, SOUP_SAME_SITE_POLICY_LAX);
  if (sameSite == 3) soup_cookie_set_same_site_policy(cookie, SOUP_SAME_SITE_POLICY_STRICT);
#endif
  if (expires >= 0) {
#if SOUP_CHECK_VERSION(2, 99, 0)
    GDateTime *date = g_date_time_new_from_unix_utc((gint64)expires);
    soup_cookie_set_expires(cookie, date);
    g_date_time_unref(date);
#else
    SoupDate *date = soup_date_new_from_time_t((time_t)expires);
    soup_cookie_set_expires(cookie, date);
    soup_date_free(date);
#endif
  }
  CookieWork *work = g_new0(CookieWork, 1);
  work->call = call;
  work->cookie = cookie;
  later(setCookieOnMain, work);
}

static void cleared(GObject *source, GAsyncResult *result, gpointer data) {
  AsyncWork *work = data;
  webkit_website_data_manager_clear_finish(manager, result, NULL);
  actBrowserDone(work->call, 1);
  freeWork(work);
}

static gboolean clearOnMain(gpointer data) {
  AsyncWork *work = data;
  if (!manager) {
    actBrowserDone(work->call, 1);
    freeWork(work);
    return G_SOURCE_REMOVE;
  }
  webkit_website_data_manager_clear(manager, WEBKIT_WEBSITE_DATA_ALL, 0, NULL, cleared, work);
  return G_SOURCE_REMOVE;
}

void actBrowserClearData(uint64_t call) {
  AsyncWork *work = g_new0(AsyncWork, 1);
  work->call = call;
  later(clearOnMain, work);
}
