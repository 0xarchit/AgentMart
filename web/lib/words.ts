// Turns the database's own words into words a shopper would use. The values are
// checked into the schema, so the mapping is exhaustive by test rather than by
// hope, and anything unmapped degrades to readable text instead of snake case.

// Every value the two money columns are allowed to hold. Keeping the lists here
// is what lets a test fail when a new one is added and left unworded.
export const orderStatuses = [
  "pending",
  "fulfilled_via_wallet",
  "failed",
  "refunded_via_wallet",
] as const;

export const ledgerEntryTypes = [
  "topup",
  "purchase_debit",
  "purchase_refund",
] as const;

// Every outcome run_summary can settle on. The first three are the shopping pass's
// own words; the rest are what that view reads back off a cancellation or a run
// that broke before it could decide. Listed here for the same reason as the two
// above: a new one added and left unworded fails a test rather than reaching a
// person as snake case.
export const runOutcomes = [
  "buy",
  "ask_human",
  "declined",
  "refunded",
  "refund_refused",
  "failed",
] as const;

const wording: Record<string, string> = {
  pending: "waiting",
  fulfilled_via_wallet: "paid from your balance",
  failed: "did not go through",
  refunded_via_wallet: "refunded to your balance",
  topup: "money added",
  purchase_debit: "paid for an order",
  purchase_refund: "refunded to you",
  buy: "bought",
  ask_human: "waiting for your tap",
  declined: "did not buy",
  refunded: "refunded to your balance",
};

/** plainWords renders one stored value for a person to read. */
export function plainWords(value: string): string {
  const known = wording[value];
  if (known) {
    return known;
  }
  return value.replace(/_/g, " ").trim() || "unknown";
}

/**
 * spendLimitAction names what happened to the ceiling the agents may spend under
 * without asking. The direction is in the action rather than only in the payload
 * because the account owner's own dashboard lists the action and not the payload,
 * and "your agent's ceiling went up" is the half of this a person needs to see.
 * Getting the comparison backwards would record a widening of spending authority
 * as a narrowing, which is why it is a named function with a test rather than a
 * ternary inside a route.
 */
export function spendLimitAction(fromPaise: number, toPaise: number): string {
  if (fromPaise === toPaise) return "spend_limit_unchanged";
  return toPaise > fromPaise ? "spend_limit_raised" : "spend_limit_lowered";
}
