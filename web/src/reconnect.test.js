import { describe, it, expect } from "vitest";
import { reconnectDelaySeconds, shouldReconnect, TERMINAL_POWERED_OFF_CODE } from "./reconnect";

describe("reconnectDelaySeconds", () => {
  it("doubles from 1 second per attempt", () => {
    expect([0, 1, 2, 3, 4, 5].map(reconnectDelaySeconds)).toEqual([1, 2, 4, 8, 16, 32]);
  });
  it("caps at 60 seconds", () => {
    expect(reconnectDelaySeconds(6)).toBe(60);
    expect(reconnectDelaySeconds(20)).toBe(60);
  });
});

describe("shouldReconnect", () => {
  it("does not reconnect after a powered-off close", () => {
    expect(shouldReconnect(TERMINAL_POWERED_OFF_CODE)).toBe(false);
  });
  it("reconnects on an ordinary drop", () => {
    expect(shouldReconnect(1006)).toBe(true); // abnormal closure (network blip)
    expect(shouldReconnect(1001)).toBe(true); // going away (container restart)
    expect(shouldReconnect(undefined)).toBe(true); // no code -> heal by reconnecting
  });
});
