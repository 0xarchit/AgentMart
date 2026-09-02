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

const wording: Record<string, string> = {
  pending: "waiting",
  fulfilled_via_wallet: "paid from your balance",
  failed: "did not go through",
  refunded_via_wallet: "refunded to your balance",
  topup: "money added",
  purchase_debit: "paid for an order",
  purchase_refund: "refunded to you",
};

/** plainWords renders one stored value for a person to read. */
export function plainWords(value: string): string {
  const known = wording[value];
  if (known) {
    return known;
  }
  return value.replace(/_/g, " ").trim() || "unknown";
}
