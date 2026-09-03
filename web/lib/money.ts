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

/**
 * ledgerMoney renders one wallet movement with the direction it went. Every
 * wallet_ledger row is stored positive, the column carries a `> 0` constraint,
 * so the sign has to come from the kind of entry rather than from the number.
 * Reading the number alone printed a purchase as money arriving.
 */
export function ledgerMoney(entryType: string, paise: number): string {
  return `${entryType === "purchase_debit" ? "-" : "+"}${money(paise)}`;
}
