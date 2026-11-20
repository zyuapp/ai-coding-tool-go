#ifndef ACT_BROWSER_LINUX_H
#define ACT_BROWSER_LINUX_H

#include <stdint.h>

typedef struct {
  char *url;
  char *title;
  int loading;
  int canGoBack;
  int canGoForward;
} ACTBrowserState;

/* Every function runs its work on the GTK main thread. Strings handed back are malloc'd for the caller. */
char *actBrowserStart(const char *sessionDir, const char *diagnostics, const char *mouseNavigation, const char *handlerName);
void actBrowserStop(void);
char *actBrowserOpen(uint64_t view, double width, double height);
char *actBrowserLoad(uint64_t view, const char *url);
void actBrowserBack(uint64_t view);
void actBrowserForward(uint64_t view);
void actBrowserReload(uint64_t view);
void actBrowserClose(uint64_t view);
void actBrowserPlace(uint64_t view, int shown, double x, double y, double width, double height);
double actBrowserZoom(void);
void actBrowserState(uint64_t view, ACTBrowserState *out);
void actBrowserFocus(uint64_t view);

/* Asynchronous: each answers through its Go callback with the call number it was given. */
void actBrowserEvaluate(uint64_t view, const char *script, uint64_t call);
void actBrowserDraw(uint64_t view, int fullPage, double width, double height, uint64_t call);
void actBrowserFind(uint64_t view, const char *query, int backwards, uint64_t call);
void actBrowserCookies(uint64_t call);
void actBrowserDeleteCookie(uint64_t snapshot, int index, uint64_t call);
void actBrowserSetCookie(const char *name, const char *value, const char *domain, const char *path, int secure, int httpOnly, int sameSite, double expires, uint64_t call);
void actBrowserClearData(uint64_t call);

#endif
