// Tests for the shopper facing wording of stored values.
import { describe, expect, it } from "vitest";
import {
  ledgerEntryTypes,
  orderStatuses,
  plainWords,
  spendLimitAction,
} from "./words";

describe("plainWords", () => {
  it("words every value the schema allows", () => {
    // A value added to the database and left unworded would otherwise reach a
    // person as snake case, which is what this is here to stop.
    for (const value of [...orderStatuses, ...ledgerEntryTypes]) {
      const worded = plainWords(value);
      expect(worded).not.toContain("_");
      expect(worded).not.toBe(value);
    }
  });

  it("says what happened rather than what the column holds", () => {
    expect(plainWords("fulfilled_via_wallet")).toBe("paid from your balance");
    expect(plainWords("refunded_via_wallet")).toBe("refunded to your balance");
    expect(plainWords("topup")).toBe("money added");
  });

  it("degrades to readable text for anything unmapped", () => {
    expect(plainWords("some_future_state")).toBe("some future state");
  });

  it("never renders an empty label", () => {
    expect(plainWords("")).toBe("unknown");
    expect(plainWords("__")).toBe("unknown");
  });
});

describe("spendLimitAction", () => {
  it("names a widening of the agent's authority as a raise", () => {
    expect(spendLimitAction(250_000, 500_000)).toBe("spend_limit_raised");
  });

  it("names a narrowing as a lowering", () => {
    expect(spendLimitAction(500_000, 250_000)).toBe("spend_limit_lowered");
  });

  it("does not claim a change when the ceiling is re-set to itself", () => {
    expect(spendLimitAction(250_000, 250_000)).toBe("spend_limit_unchanged");
  });

  it("reads as plain words on the owner's own dashboard", () => {
    // The wording map does not carry these, and it does not need to: the fallback
    // is what a person reads, so it has to be readable.
    expect(plainWords(spendLimitAction(1, 2))).toBe("spend limit raised");
  });
});
