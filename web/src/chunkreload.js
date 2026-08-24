// Recovering from a deploy that happened while a tab was open.
//
// Vite gives every chunk a content hash, so a deploy replaces AppEditor-abc.js
// with AppEditor-xyz.js. A tab loaded before the deploy still holds the OLD
// names, and the moment it opens a tab whose chunk it has not fetched yet, that
// file is gone. The page then breaks in a way that says nothing useful --
// "error loading dynamically imported module" -- and the only cure a person
// finds is reloading, which they have to think of themselves.
//
// So: reload for them, ONCE. A reload fetches the current index.html and the
// current chunk names, which is exactly the fix a person would apply.
//
// The once matters. If the chunk is missing for any other reason -- a broken
// deploy, a proxy serving nonsense -- reloading again would spin forever, so a
// marker in sessionStorage means the second failure in a row is allowed to
// surface as a real error instead.

const RELOADED = "hostit:chunk-reloaded";

// retryChunk wraps a dynamic import so a failure caused by a stale bundle heals
// itself. Everything else is rethrown untouched.
export function retryChunk(load) {
  return () =>
    load().catch((err) => {
      if (!isStaleChunk(err) || alreadyReloaded()) {
        throw err;
      }
      markReloaded();
      window.location.reload();
      // Never resolves: the page is going away, and resolving with nothing
      // would render an empty tab for the split second before it does.
      return new Promise(() => {});
    });
}

// clearChunkReload forgets the marker once the app has rendered, so the NEXT
// deploy gets its own single retry rather than inheriting a spent one.
export function clearChunkReload() {
  try {
    window.sessionStorage?.removeItem(RELOADED);
  } catch {
    // Private mode, or storage disabled. The marker is an optimisation; without
    // it the only cost is that a genuinely broken chunk reloads once more.
  }
}

// isStaleChunk recognises a failed dynamic import. Browsers phrase it
// differently -- Firefox blames the MIME type, Chrome and Safari the fetch --
// so this matches on what they have in common rather than on one wording.
function isStaleChunk(err) {
  const text = String((err && (err.message || err)) || "");
  return (
    /dynamically imported module/i.test(text) ||
    /Importing a module script failed/i.test(text) ||
    /Failed to fetch dynamically imported module/i.test(text) ||
    /disallowed MIME type/i.test(text) ||
    /error loading dynamically imported module/i.test(text)
  );
}

function alreadyReloaded() {
  try {
    return window.sessionStorage?.getItem(RELOADED) === "1";
  } catch {
    return false;
  }
}

function markReloaded() {
  try {
    window.sessionStorage?.setItem(RELOADED, "1");
  } catch {
    // See clearChunkReload: losing the marker costs one extra reload at worst.
  }
}
