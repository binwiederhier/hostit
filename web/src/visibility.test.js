import { describe, expect, it } from "vitest";
import { visibilityOf } from "./components";

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
