import { describe, expect, it } from "vitest";
import { visibilityOf } from "./components";
import { visibilityChanges } from "./visibility";

// "Restricted" is not a fourth setting the user picks -- it is what private
// looks like once somebody else has been let in. Deriving it in one place is
// what keeps the badge, the dialog and the settings row from disagreeing.
describe("visibilityOf", () => {
  it("is public when the app is not private, however many viewers exist", () => {
    expect(visibilityOf(false)).toBe("public");
    expect(visibilityOf(false, 3)).toBe("public");
  });

  it("is private when nobody else has been let in", () => {
    expect(visibilityOf(true)).toBe("private");
    expect(visibilityOf(true, 0)).toBe("private");
  });

  it("is restricted once somebody has", () => {
    expect(visibilityOf(true, 1)).toBe("restricted");
    expect(visibilityOf(true, 9)).toBe("restricted");
  });
});

describe("visibilityChanges", () => {
  const guest = { id: "u2", email: "guest@example.com" };
  const other = { id: "u3", email: "other@example.com" };

  it("sends nothing when nothing changed", () => {
    const c = visibilityChanges([guest], [guest], true, true);
    expect(c).toMatchObject({ add: [], remove: [], changed: false });
  });

  it("adds somebody who is only in the draft", () => {
    const c = visibilityChanges([guest], [guest, { email: "new@example.com" }], true, true);
    expect(c.add).toEqual(["new@example.com"]);
    expect(c.remove).toEqual([]);
    expect(c.changed).toBe(true);
  });

  it("removes somebody dropped from the draft", () => {
    const c = visibilityChanges([guest, other], [guest], true, true);
    expect(c.add).toEqual([]);
    expect(c.remove).toEqual(["u3"]);
  });

  it("handles an add and a remove together", () => {
    const c = visibilityChanges([guest], [{ email: "new@example.com" }], true, true);
    expect(c.add).toEqual(["new@example.com"]);
    expect(c.remove).toEqual(["u2"]);
  });

  it("counts a visibility flip on its own as a change", () => {
    expect(visibilityChanges([], [], false, true).changed).toBe(true);
    expect(visibilityChanges([], [], true, true).changed).toBe(false);
  });

  // The dialog can be opened before the viewer list has arrived. Reading that
  // as "there were none" would make Save revoke everyone who actually has
  // access, and report success while doing it.
  it("never removes anybody while the list is still loading", () => {
    const c = visibilityChanges(null, [], true, true);
    expect(c.remove).toEqual([]);
    expect(c.changed).toBe(false);
  });

  it("still allows adding while the list is loading", () => {
    const c = visibilityChanges(null, [{ email: "new@example.com" }], true, true);
    expect(c.add).toEqual(["new@example.com"]);
    expect(c.remove).toEqual([]);
  });
});
