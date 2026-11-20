#ifndef ACT_NATIVE_DARWIN_H
#define ACT_NATIVE_DARWIN_H

int actScreens(double *out, int capacity);
int actWindowState(double *frame, int *flags);
void actPlaceAndShow(double x, double y, double width, double height, int hasPosition, int maximized, int fullScreen);
void actReveal(void);
void actToggleFullScreen(void);
void actMinimize(void);
void actZoomWindow(void);
void actBringAllToFront(void);
void actAbout(void);
void actHideOthers(void);
void actShowAll(void);
void actHideApp(void);
void actSetBackground(int r, int g, int b, int a);
void actSetAppearance(int mode);
void actZoomPage(int step);
void actFocusPage(void);
void actToggleInspector(void);
void actSetDockUnread(int unread);
void actInstallWindowHooks(int material);
int actGlassAvailable(void);
double actGlassTint(void);
int actReducedTransparency(void);
void actWatchTransparency(void);
void actSetBadge(int count);
void actTitlebarDoubleClick(void);
char *actFontFamilies(void);

#endif
