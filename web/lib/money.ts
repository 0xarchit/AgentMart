// The one rupee formatter. Every figure a person reads goes through this, so no
// page can disagree with another about what an amount was.
//
// Two fraction digits, fixed rather than capped. Paise are the unit money moves
// in: a cap drops them off a settled amount silently, and a cap with no floor
// renders 314920 paise as "3,149.2", which reads like a truncated number rather
// than a price.

/** money renders paise as rupees, the only currency this project handles. */
export function money(paise: number): string {
  return `₹${(paise / 100).toLocaleString("en-IN", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`;
}
