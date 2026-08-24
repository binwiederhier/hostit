import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearChunkReload, retryChunk } from "./chunkreload";

// A browser module tested in Node. Rather than pull in a whole DOM for one
// file, the two globals it touches are supplied here -- which also proves the
// module reaches for them defensively rather than assuming a browser.
describe("retryChunk", () => {
  let reload;
  let store;
  beforeEach(() => {
    reload = vi.fn();
    store = new Map();
    globalThis.window = {
      location: { reload },
      sessionStorage: {
        getItem: (k) => (store.has(k) ? store.get(k) : null),
        setItem: (k, v) => store.set(k, String(v)),
        removeItem: (k) => store.delete(k),
        clear: () => store.clear(),
      },
    };
  });
  afterEach(() => {
    delete globalThis.window;
  });

  it("passes a successful import straight through", async () => {
    const mod = { default: () => null };
    await expect(retryChunk(async () => mod)()).resolves.toBe(mod);
    expect(reload).not.toHaveBeenCalled();
  });

  // The case this exists for: a deploy replaced the bundle while the tab was
  // open, so the chunk name it holds no longer exists.
  it("reloads once when a chunk has gone missing", async () => {
    const load = vi.fn().mockRejectedValue(
      new TypeError("error loading dynamically imported module: /static/media/AppEditor-abc.js")
    );
    const pending = retryChunk(load)();
    await Promise.resolve();
    expect(reload).toHaveBeenCalledTimes(1);
    // It never resolves: the page is going away, and resolving with nothing
    // would render an empty tab for the split second before it does.
    let settled = false;
    pending.then(() => (settled = true), () => (settled = true));
    await Promise.resolve();
    expect(settled).toBe(false);
  });

  it("recognises the way each browser phrases it", async () => {
    for (const message of [
      "Failed to fetch dynamically imported module: https://x/static/media/a.js",
      "Importing a module script failed.",
      'Loading module from "https://x/a.js" was blocked because of a disallowed MIME type ("text/html")',
    ]) {
      store.clear();
      reload.mockClear();
      retryChunk(async () => {
        throw new TypeError(message);
      })();
      await Promise.resolve();
      expect(reload, message).toHaveBeenCalledTimes(1);
    }
  });

  // Reloading on every failure would spin forever against a genuinely broken
  // deploy, so the second one in a row is allowed to surface.
  it("does not reload twice in a row", async () => {
    const boom = new TypeError("error loading dynamically imported module: /a.js");
    retryChunk(async () => {
      throw boom;
    })();
    await Promise.resolve();
    expect(reload).toHaveBeenCalledTimes(1);

    await expect(
      retryChunk(async () => {
        throw boom;
      })()
    ).rejects.toThrow(/dynamically imported module/);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("rethrows anything that is not a stale chunk", async () => {
    await expect(
      retryChunk(async () => {
        throw new Error("the component itself threw");
      })()
    ).rejects.toThrow("the component itself threw");
    expect(reload).not.toHaveBeenCalled();
  });

  it("clears the marker so the next deploy gets its own retry", async () => {
    store.set("hostit:chunk-reloaded", "1");
    clearChunkReload();
    expect(store.has("hostit:chunk-reloaded")).toBe(false);
  });
});
