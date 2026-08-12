import { describe, it, expect } from "vitest";
import { reduceChatEvent, filesFromClipboard } from "./chat";

describe("filesFromClipboard", () => {
  it("returns the pasted files (e.g. a screenshot in clipboardData.files)", () => {
    const img = { name: "shot.png", type: "image/png" };
    expect(filesFromClipboard({ files: [img] })).toEqual([img]);
  });
  it("falls back to items of kind 'file' when files is empty", () => {
    const img = { name: "x.png", type: "image/png" };
    const cd = {
      files: [],
      items: [
        { kind: "string", getAsFile: () => null },
        { kind: "file", getAsFile: () => img },
      ],
    };
    expect(filesFromClipboard(cd)).toEqual([img]);
  });
  it("returns [] for a plain-text paste and for null", () => {
    expect(filesFromClipboard({ files: [], items: [{ kind: "string", getAsFile: () => null }] })).toEqual([]);
    expect(filesFromClipboard(null)).toEqual([]);
  });
});

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

import { formatTokens } from "./chat";

describe("formatTokens", () => {
  it("shows exact counts below 1000", () => {
    expect(formatTokens(0)).toBe("0");
    expect(formatTokens(42)).toBe("42");
    expect(formatTokens(999)).toBe("999");
  });
  it("abbreviates thousands and trims a trailing .0", () => {
    expect(formatTokens(1000)).toBe("1k");
    expect(formatTokens(1234)).toBe("1.2k");
    expect(formatTokens(12345)).toBe("12.3k");
  });
  it("treats missing or negative as 0", () => {
    expect(formatTokens(undefined)).toBe("0");
    expect(formatTokens(null)).toBe("0");
    expect(formatTokens(-5)).toBe("0");
  });
});

import { formatDuration } from "./chat";

describe("formatDuration", () => {
  it("shows seconds under a minute", () => {
    expect(formatDuration(0)).toBe("0s");
    expect(formatDuration(45)).toBe("45s");
    expect(formatDuration(59)).toBe("59s");
  });
  it("shows minutes and seconds under an hour", () => {
    expect(formatDuration(60)).toBe("1m 0s");
    expect(formatDuration(303)).toBe("5m 3s");
  });
  it("shows hours and minutes past an hour", () => {
    expect(formatDuration(3723)).toBe("1h 2m");
  });
});
