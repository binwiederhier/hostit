// The app-detail tabs a user may choose to show or hide, in canonical render
// order. Mirrors control/tabs.go: the server is authoritative on writes, and
// this keeps the live UI in step. The always-on tabs (connections, settings,
// snapshots) are not user-toggleable and are not in this set.
export const TOGGLEABLE_TABS = ["assistant", "files", "terminal", "logs"];

export const TAB_LABELS = {
  assistant: "Assistant",
  files: "Files",
  terminal: "Terminal",
  logs: "Logs",
};

// normalizeTabs cleans a tab set to canonical form and guarantees a primary
// pane: the assistant counts only when it is enabled, otherwise files is forced
// on. An empty input returns [] (meaning "no override -- use the default").
export function normalizeTabs(list, assistantEnabled) {
  const want = new Set((list || []).map((s) => String(s).trim()).filter(Boolean));
  if (want.size === 0) return [];
  const out = TOGGLEABLE_TABS.filter((k) => want.has(k) && (k !== "assistant" || assistantEnabled));
  const hasPrimary = (assistantEnabled && out.includes("assistant")) || out.includes("files");
  if (!hasPrimary) out.unshift("files");
  return out;
}

export const tabsFromCsv = (csv) => (csv || "").split(",").map((s) => s.trim()).filter(Boolean);
export const tabsToCsv = (list) => (list || []).join(",");

// resolveTabs decides which toggleable tabs show for an app: the app's own
// override wins; otherwise the viewer's profile default; otherwise the built-in
// default (everything available). Always normalized.
export function resolveTabs(appTabsCsv, profileTabsCsv, assistantEnabled) {
  const app = tabsFromCsv(appTabsCsv);
  if (app.length) return normalizeTabs(app, assistantEnabled);
  const profile = tabsFromCsv(profileTabsCsv);
  if (profile.length) return normalizeTabs(profile, assistantEnabled);
  return normalizeTabs(TOGGLEABLE_TABS, assistantEnabled);
}

// The three self-selected technical levels, in the order the welcome modal shows
// them (least technical first, per the design).
export const TECH_LEVELS = ["novice", "intermediate", "expert"];

export const TECH_LABELS = {
  novice: "Not technical at all",
  intermediate: "Somewhat technical",
  expert: "Very technical",
};

// presetTabs is the default tab set for a technical level (item 7): a novice
// sees just the assistant; the more technical, the more panes.
export function presetTabs(level, assistantEnabled) {
  switch (level) {
    case "novice":
      return normalizeTabs(["assistant"], assistantEnabled);
    case "intermediate":
      return normalizeTabs(["assistant", "files", "logs"], assistantEnabled);
    case "expert":
      return normalizeTabs(["assistant", "files", "terminal", "logs"], assistantEnabled);
    default:
      return [];
  }
}

// presetPrompt is the starter assistant prompt for a technical level (item 7),
// pre-filled so the assistant matches how the person wants to be spoken to.
export function presetPrompt(level) {
  switch (level) {
    case "novice":
      return "I am not very technical. Explain what you are doing in plain language, avoid jargon, and prefer making the change for me over handing me instructions.";
    case "intermediate":
      return "I am somewhat technical. You can use technical terms, but briefly explain the non-obvious steps.";
    case "expert":
      return "I am very technical. Be concise and skip the basic explanations.";
    default:
      return "";
  }
}
