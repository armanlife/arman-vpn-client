#include <cstdlib>
#include <cstring>

#include <gtk/gtk.h>

#include "my_application.h"

int main(int argc, char** argv) {
  // X11 (XWayland) first, Wayland only without it, on every desktop: Flutter
  // 3.50's Wayland renderer can hang at startup (flutter/flutter#196529,
  // seen on COSMIC, niri and labwc) and draws at the wrong size after a
  // display-scale change (flutter/flutter#196476); through XWayland neither
  // happens. GDK_BACKEND, when set, still decides. Back to GTK's own choice
  // once a stable Flutter has both fixes.
  if (getenv("GDK_BACKEND") == nullptr) {
    gdk_set_allowed_backends("x11,wayland");
  }

  // A copy started with its own data folder (development and tests) runs
  // beside the installed one.
  gboolean unique = TRUE;
  for (int i = 1; i < argc; i++) {
    if (strncmp(argv[i], "--data-dir=", 11) == 0) {
      unique = FALSE;
    }
  }
  g_autoptr(MyApplication) app = my_application_new(unique);
  return g_application_run(G_APPLICATION(app), argc, argv);
}
