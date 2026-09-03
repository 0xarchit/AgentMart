// Tests for the one rupee formatter every page reads its figures through.
import { describe, expect, it } from "vitest";
import { money } from "./money";

describe("money", () => {
  it("keeps the paise on a settled amount", () => {
    // A capped formatter rendered this as "₹3,149", so the page and the ledger
    // disagreed about what the agent actually paid.
    expect(money(314920)).toBe("₹3,149.20");
  });

  it("shows both digits on a round amount rather than one", () => {
    expect(money(100000)).toBe("₹1,000.00");
    expect(money(314900)).toBe("₹3,149.00");
  });

  it("groups the Indian way, so a lakh is not written as 100,000", () => {
    expect(money(10_000_000)).toBe("₹1,00,000.00");
  });

  it("renders an empty wallet as a figure rather than as nothing", () => {
    expect(money(0)).toBe("₹0.00");
  });

  it("rounds a half paise rather than truncating it", () => {
    expect(money(1)).toBe("₹0.01");
  });
});
