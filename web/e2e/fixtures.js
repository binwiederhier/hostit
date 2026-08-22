import { test as base, expect } from "@playwright/test";

// Every spec fails on an uncaught exception or a console error from our own
// pages, without each one having to remember to check.
//
// The bug this exists for: a component referenced a helper nobody imported.
// The bundle built, the unit tests passed, the page rendered -- and clicking
// one button threw "visibilityChanges is not defined". Nothing in the pipeline
// noticed, because a missing import is a runtime error and only on the path
// that uses it. Driving the UI is the only thing that catches that class, and
// it only catches it if a thrown error actually fails the test.
//
// Errors from tenant apps are ignored on purpose: the dashboard embeds live
// previews of arbitrary user code, whose console noise is not ours to fix.
export const test = base.extend({
  page: async ({ page }, use) => {
    const errors = [];
    page.on("pageerror", (e) => errors.push(`uncaught: ${e}`));
    page.on("console", (m) => {
      if (m.type() !== "error") return;
      const from = m.location()?.url || "";
      if (from && !sameOrigin(from, page.url())) return; // a tenant app's iframe
      // The browser logs every non-2xx response as a console error. That is an
      // HTTP status, not a JavaScript fault, and specs that exercise a failure
      // path on purpose assert on it directly. Uncaught exceptions still come
      // through pageerror, which is what this fixture is really for.
      if (/^Failed to load resource/.test(m.text())) return;
      errors.push(m.text());
    });

    await use(page);

    expect(errors, `JavaScript errors on the page:\n  ${errors.join("\n  ")}`).toEqual([]);
  },
});

// Unparseable counts as ours: better a noisy failure than a quiet exemption.
function sameOrigin(a, b) {
  try {
    return new URL(a).origin === new URL(b).origin;
  } catch {
    return true;
  }
}

export { expect };
