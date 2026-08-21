import { describe, expect, it } from "vitest";
import { limitInputs, limitsPatchBody } from "./limits";

describe("limitInputs", () => {
  it("shows overrides as editable values and inherited fields as empty", () => {
    expect(limitInputs({ memory_mb: 512, disk_mb: 0, cpu_milli: 1500 })).toEqual({
      memory: "512",
      disk: "",
      cpu: "1.5", // millicores render as cores in the UI
    });
    expect(limitInputs(undefined)).toEqual({ memory: "", disk: "", cpu: "" });
  });
});

describe("limitsPatchBody", () => {
  const overrides = { memory_mb: 512, disk_mb: 0, cpu_milli: 1500 };

  it("sends 0 (skip) for untouched empty fields and values for filled ones", () => {
    expect(limitsPatchBody({ memory: "512", disk: "4096", cpu: "1.5" }, overrides)).toEqual({
      memory_mb: 512,
      disk_mb: 4096,
      cpu_milli: 1500,
    });
  });

  it("clears an existing override when its field is emptied", () => {
    expect(limitsPatchBody({ memory: "", disk: "", cpu: "" }, overrides)).toEqual({
      memory_mb: -1, // had an override, now empty: clear it
      disk_mb: 0, // never overridden and still empty: leave alone
      cpu_milli: -1,
    });
  });

  it("converts cores to millicores and rejects garbage as skip", () => {
    expect(limitsPatchBody({ memory: "x", disk: "", cpu: "0.25" }, {}).cpu_milli).toBe(250);
    expect(limitsPatchBody({ memory: "x", disk: "", cpu: "" }, {}).memory_mb).toBe(0);
  });
});
