import { describe, it, expect } from "vitest";
import { reconnectDelaySeconds } from "./reconnect";

describe("reconnectDelaySeconds", () => {
  it("doubles from 1 second per attempt", () => {
    expect([0, 1, 2, 3, 4, 5].map(reconnectDelaySeconds)).toEqual([1, 2, 4, 8, 16, 32]);
  });
  it("caps at 60 seconds", () => {
    expect(reconnectDelaySeconds(6)).toBe(60);
    expect(reconnectDelaySeconds(20)).toBe(60);
  });
});
