import { describe, expect, it } from "vitest";
import { cores, pairMB, usageLevel } from "./components";

describe("usageLevel", () => {
  it("keeps the shared 75/90 knees and stays quiet without a limit", () => {
    expect(usageLevel(50, 100)).toBe("");
    expect(usageLevel(75, 100)).toBe("warn");
    expect(usageLevel(89, 100)).toBe("warn");
    expect(usageLevel(90, 100)).toBe("crit");
    expect(usageLevel(300, 0)).toBe("", "no limit, no judgement");
  });
});

describe("pairMB", () => {
  it("uses one unit per pair, GB from 1 GB up", () => {
    expect(pairMB(12, 512)).toBe("12/512 MB");
    expect(pairMB(512, 2048)).toBe("0.5/2 GB");
    expect(pairMB(300, 0)).toBe("300 MB");
  });
});

describe("cores", () => {
  it("says core once and cores otherwise -- 1 core is the shipped default", () => {
    expect(cores(1)).toBe("1 core");
    expect(cores(0.5)).toBe("0.5 cores");
    expect(cores(2)).toBe("2 cores");
  });
});
