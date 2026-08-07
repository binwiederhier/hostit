import { describe, it, expect } from "vitest";
import { reduceChatEvent } from "./chat";

describe("reduceChatEvent", () => {
  it("appends a tool_use as a pending tool without refreshing the preview", () => {
    const { items, refreshPreview } = reduceChatEvent([], { type: "tool_use", tool: "deploy", input: "{}" });
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "tool", tool: "deploy", output: null });
    // Requesting a deploy must NOT reload the preview -- the new content is not live yet.
    expect(refreshPreview).toBe(false);
  });

  it("refreshes the preview only when a deploy tool_result completes", () => {
    const pending = reduceChatEvent([], { type: "tool_use", tool: "deploy", input: "{}" }).items;
    const { items, refreshPreview } = reduceChatEvent(pending, { type: "tool_result", output: "deployed", is_error: false });
    expect(items[0].output).toBe("deployed");
    expect(refreshPreview).toBe(true);
  });

  it("also refreshes on a refresh_preview tool_result", () => {
    const pending = reduceChatEvent([], { type: "tool_use", tool: "refresh_preview", input: "{}" }).items;
    const { refreshPreview } = reduceChatEvent(pending, { type: "tool_result", output: "ok", is_error: false });
    expect(refreshPreview).toBe(true);
  });

  it("does not refresh when the deploy tool_result is an error", () => {
    const pending = reduceChatEvent([], { type: "tool_use", tool: "deploy", input: "{}" }).items;
    const { refreshPreview } = reduceChatEvent(pending, { type: "tool_result", output: "boom", is_error: true });
    expect(refreshPreview).toBe(false);
  });

  it("does not refresh for a non-preview tool completing", () => {
    const pending = reduceChatEvent([], { type: "tool_use", tool: "read_logs", input: "{}" }).items;
    const { refreshPreview } = reduceChatEvent(pending, { type: "tool_result", output: "...", is_error: false });
    expect(refreshPreview).toBe(false);
  });

  it("folds a tool_result onto the newest pending tool and keeps earlier ones", () => {
    let items = [];
    items = reduceChatEvent(items, { type: "tool_use", tool: "read_logs", input: "{}" }).items;
    items = reduceChatEvent(items, { type: "tool_use", tool: "deploy", input: "{}" }).items;
    const res = reduceChatEvent(items, { type: "tool_result", output: "deployed", is_error: false });
    expect(res.items[0].output).toBeNull(); // read_logs still pending
    expect(res.items[1].output).toBe("deployed"); // deploy completed
    expect(res.refreshPreview).toBe(true);
  });

  it("appends user, text, and non-empty thinking; drops blank thinking", () => {
    let items = reduceChatEvent([], { type: "user", text: "hi" }).items;
    items = reduceChatEvent(items, { type: "text", text: "hello" }).items;
    items = reduceChatEvent(items, { type: "thinking", text: "   " }).items; // blank -> dropped
    items = reduceChatEvent(items, { type: "thinking", text: "pondering" }).items;
    expect(items.map((i) => i.kind)).toEqual(["user", "text", "thinking"]);
  });
});
