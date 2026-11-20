//go:build linux && cgo

package desktop

/*
#cgo linux pkg-config: gtk+-3.0
#cgo !webkit2_41 pkg-config: webkit2gtk-4.0
#cgo webkit2_41 pkg-config: webkit2gtk-4.1
#cgo LDFLAGS: -lm
#include <math.h>
#include <string.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

// The app's own page among the window's web views: the browser panel lays its pages over it.
static WebKitWebView *findAppView(GtkWidget *widget) {
  if (WEBKIT_IS_WEB_VIEW(widget)) {
    const char *uri = webkit_web_view_get_uri(WEBKIT_WEB_VIEW(widget));
    return uri && strncmp(uri, "wails:", 6) == 0 ? WEBKIT_WEB_VIEW(widget) : NULL;
  }
  if (!GTK_IS_CONTAINER(widget)) return NULL;
  GList *children = gtk_container_get_children(GTK_CONTAINER(widget));
  WebKitWebView *found = NULL;
  for (GList *child = children; child && !found; child = child->next) found = findAppView(child->data);
  g_list_free(children);
  return found;
}

static WebKitWebView *appView(void) {
  GList *windows = gtk_window_list_toplevels();
  WebKitWebView *found = NULL;
  for (GList *item = windows; item && !found; item = item->next) found = findAppView(item->data);
  g_list_free(windows);
  return found;
}

static double zoomLevel;

static gboolean zoomOnMain(gpointer data) {
  int step = GPOINTER_TO_INT(data);
  WebKitWebView *view = appView();
  if (view) {
    zoomLevel = step == 0 ? 0 : fmax(-8, fmin(9, zoomLevel + step * 0.5));
    webkit_web_view_set_zoom_level(view, pow(1.2, zoomLevel));
  }
  return G_SOURCE_REMOVE;
}

static gboolean focusOnMain(gpointer data) {
  WebKitWebView *view = appView();
  if (view) gtk_widget_grab_focus(GTK_WIDGET(view));
  return G_SOURCE_REMOVE;
}

static gboolean inspecting;

static void inspectorClosed(WebKitWebInspector *inspector, gpointer data) { inspecting = FALSE; }

static gboolean inspectorOnMain(gpointer data) {
  WebKitWebView *view = appView();
  if (!view) return G_SOURCE_REMOVE;
  webkit_settings_set_enable_developer_extras(webkit_web_view_get_settings(view), TRUE);
  WebKitWebInspector *inspector = webkit_web_view_get_inspector(view);
  if (!g_object_get_data(G_OBJECT(inspector), "aicodingtool-watched")) {
    g_signal_connect(inspector, "closed", G_CALLBACK(inspectorClosed), NULL);
    g_object_set_data(G_OBJECT(inspector), "aicodingtool-watched", GINT_TO_POINTER(1));
  }
  if (inspecting) {
    webkit_web_inspector_close(inspector);
    inspecting = FALSE;
    gtk_widget_grab_focus(GTK_WIDGET(view));
  } else {
    webkit_web_inspector_show(inspector);
    inspecting = TRUE;
  }
  return G_SOURCE_REMOVE;
}

// Clipboard reads wait on nested main loops, so they run on the main thread while the caller waits.
typedef struct {
  char *result;
  gboolean done;
  GMutex lock;
  GCond cond;
} ClipboardWork;

static void finish(ClipboardWork *work, char *result) {
  g_mutex_lock(&work->lock);
  work->result = result;
  work->done = TRUE;
  g_cond_signal(&work->cond);
  g_mutex_unlock(&work->lock);
}

static gboolean filesOnMain(gpointer data) {
  gchar **uris = gtk_clipboard_wait_for_uris(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD));
  GString *paths = g_string_new("");
  for (gchar **uri = uris; uri && *uri; uri++) {
    gchar *path = g_filename_from_uri(*uri, NULL, NULL);
    if (!path) continue;
    if (paths->len) g_string_append_c(paths, '\n');
    g_string_append(paths, path);
    g_free(path);
  }
  g_strfreev(uris);
  finish(data, g_string_free(paths, FALSE));
  return G_SOURCE_REMOVE;
}

static gboolean imageOnMain(gpointer data) {
  GdkPixbuf *image = gtk_clipboard_wait_for_image(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD));
  gchar *png = NULL;
  gsize size = 0;
  if (image) {
    if (!gdk_pixbuf_save_to_buffer(image, &png, &size, "png", NULL, NULL)) png = NULL;
    g_object_unref(image);
  }
  finish(data, png ? g_base64_encode((const guchar *)png, size) : g_strdup(""));
  g_free(png);
  return G_SOURCE_REMOVE;
}

// What read leaves, which the caller frees with g_free.
static char *readClipboard(GSourceFunc read) {
  ClipboardWork work = {0};
  g_mutex_init(&work.lock);
  g_cond_init(&work.cond);
  g_idle_add(read, &work);
  g_mutex_lock(&work.lock);
  while (!work.done) g_cond_wait(&work.cond, &work.lock);
  g_mutex_unlock(&work.lock);
  g_mutex_clear(&work.lock);
  g_cond_clear(&work.cond);
  return work.result;
}

static char *actPasteboardFiles(void) { return readClipboard(filesOnMain); }
static char *actPasteboardImage(void) { return readClipboard(imageOnMain); }

static void actZoomPage(int step) { g_idle_add(zoomOnMain, GINT_TO_POINTER(step)); }
static void actFocusPage(void) { g_idle_add(focusOnMain, NULL); }
static void actToggleInspector(void) { g_idle_add(inspectorOnMain, NULL); }
*/
import "C"

import (
	"strings"
	"unsafe"
)

// nativeZoomPage steps the page's zoom as Chromium does, by factors of 1.2 per level in half levels; 0 resets it.
func (app *App) nativeZoomPage(step int) { C.actZoomPage(C.int(step)) }

func (app *App) nativeFocusPage() { C.actFocusPage() }

func (app *App) nativeToggleInspector() { C.actToggleInspector() }

// pasteboardFiles reads the clipboard's file URIs, as a file manager's copy leaves them.
func pasteboardFiles() []string {
	if text := takeText(C.actPasteboardFiles()); text != "" {
		return strings.Split(text, "\n")
	}
	return []string{}
}

// pasteboardImage is the clipboard's image as base64 PNG, or "" when it holds none.
func pasteboardImage() string { return takeText(C.actPasteboardImage()) }

func takeText(text *C.char) string {
	defer C.g_free(C.gpointer(unsafe.Pointer(text)))
	return C.GoString(text)
}
