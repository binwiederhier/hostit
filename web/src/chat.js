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
        // The new content is live only now that the deploy/refresh finished (and did
        // not error), so this is the moment to reload the preview.
        if (!ev.is_error && (tool === "deploy" || tool === "refresh_preview")) {
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
