// The dashboard card's mini live preview: a scaled-down desktop render of the
// app's own URL, shown as a non-interactive thumbnail. The previewability rules
// and the scale math live here so they can be unit-tested; the iframe itself is
// wired into Dashboard's AppCard.

// DESKTOP_WIDTH/HEIGHT is the viewport the preview iframe renders at before being
// scaled down, so the thumbnail looks like a desktop browser rather than a phone.
export const DESKTOP_WIDTH = 1280;
export const DESKTOP_HEIGHT = 800;

// isPreviewable is true only when the app is actually serving something worth
// showing: its container is up and the run process has not given up. A crashed
// (failed) app would only render the proxy's 502, and a powered-off app nothing.
export function isPreviewable(app) {
  return Boolean(app && app.running && app.app_state !== "failed");
}

// previewSrc is the URL to load in the preview iframe, or null when the app is
// not previewable. It prefers a verified custom domain, matching the card link.
export function previewSrc(app) {
  if (!isPreviewable(app)) {
    return null;
  }
  return app.custom_domain ? `https://${app.custom_domain}` : app.url;
}

// previewScale is the CSS transform factor that shrinks a DESKTOP_WIDTH-wide
// iframe to fit a container of the given pixel width. It returns 0 when the width
// is not known yet (before the ResizeObserver has measured), which hides the
// still-unscaled iframe instead of flashing it at full size.
export function previewScale(containerWidth, desktopWidth = DESKTOP_WIDTH) {
  if (!containerWidth || containerWidth <= 0) {
    return 0;
  }
  return containerWidth / desktopWidth;
}
