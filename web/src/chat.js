// reduceChatEvent applies one streamed assistant event to the transcript items and
// reports whether the live preview should reload. Kept pure (no React, no I/O) so
// the tricky bit -- reloading the preview only once a deploy/refresh COMPLETES, not
// when it is requested -- is unit-testable.
//
// It handles the item-producing events (user/thinking/text/tool_use/tool_result);
// the caller handles the control events (done/error) which have side effects.
export function reduceChatEvent(items, ev, model) {
  const next = [...items];
  let refreshPreview = false;

  if (ev.type === "tool_result") {
    // Fold the result onto the newest still-pending tool call.
    for (let i = next.length - 1; i >= 0; i--) {
      if (next[i].kind === "tool" && next[i].output == null) {
        const tool = next[i].tool;
        next[i] = { ...next[i], output: ev.output ?? "", isError: ev.is_error };
        // The new content is live only now that the tool finished (and did not
        // error), so this is the moment to reload the preview. Every mutating
        // tool counts: a static app's content changes the moment write_file
        // lands -- no deploy follows, so waiting for one leaves the preview
        // stale (and the assistant telling people to refresh their browser).
        const mutating = ["deploy", "refresh_preview", "write_file", "run_command", "rollback"];
        if (!ev.is_error && mutating.includes(tool)) {
          refreshPreview = true;
        }
        break;
      }
    }
    return { items: next, refreshPreview };
  }

  if (ev.type === "user") {
    next.push({ id: next.length, kind: "user", text: ev.text });
  } else if (ev.type === "notice" && (ev.text || "").trim()) {
    // A system note (e.g. External Claude fell back to the API), shown inline.
    next.push({ id: next.length, kind: "notice", text: ev.text });
  } else if (ev.type === "thinking" && (ev.text || "").trim()) {
    next.push({ id: next.length, kind: "thinking", text: ev.text });
  } else if (ev.type === "text") {
    next.push({ id: next.length, kind: "text", text: ev.text, model });
  } else if (ev.type === "tool_use") {
    next.push({ id: next.length, kind: "tool", tool: ev.tool, input: ev.input, output: null });
  }
  return { items: next, refreshPreview };
}

// formatTokens renders a token count compactly for the live counter: exact below
// 1000, then "1.2k" style (trailing ".0" trimmed). Non-positive/missing -> "0".
export function formatTokens(n) {
  if (!n || n < 0) return "0";
  if (n < 1000) return String(n);
  return (n / 1000).toFixed(1).replace(/\.0$/, "") + "k";
}

// formatDuration renders an elapsed time compactly: "45s", "5m 3s", "1h 2m".
export function formatDuration(seconds) {
  const s = Math.max(0, Math.floor(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

// filesFromClipboard extracts File objects from a paste's clipboard data, so a
// pasted screenshot (or any file) attaches the way a drag-drop or the "+" button
// does. A plain-text paste carries no files and yields [], leaving the browser's
// default paste behaviour intact.
export function filesFromClipboard(clipboardData) {
  if (!clipboardData) return [];
  const files = [];
  // Modern browsers put a pasted image directly in .files.
  if (clipboardData.files && clipboardData.files.length) {
    for (const f of clipboardData.files) files.push(f);
  }
  // Fall back to items of kind "file" when .files is empty.
  if (files.length === 0 && clipboardData.items) {
    for (const it of clipboardData.items) {
      if (it.kind === "file") {
        const f = it.getAsFile && it.getAsFile();
        if (f) files.push(f);
      }
    }
  }
  return files;
}
