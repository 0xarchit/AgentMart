// Derives the operations figures from rows. Kept apart from the page so the
// arithmetic behind every displayed number can be tested on its own.
export type OrderRow = {
  id: string;
  product_id: string;
  qty: number;
  amount_paise: number;
  status: string;
  created_at: string;
};

export type ProductRow = {
  id: string;
  name: string;
  stock: number;
  cost_paise: number;
  price_paise: number;
};

export type RevenueRow = {
  order_id: string;
  base_amount_paise: number;
  final_amount_paise: number;
  uplift_paise: number;
  credited_at: string;
};

export type Point = { label: string; value: number };

export type ProductTotal = { name: string; count: number; value: number };

/** LedgerRow is one wallet movement, used to reconcile the revenue figures. */
export type LedgerRow = {
  order_id: string | null;
  entry_type: string;
  amount_paise: number;
};

/** OfferRow is one priced offer from the trail, used for the attach rate. */
export type OfferRow = {
  run_id: string | null;
  payload: { bundle_name?: string | null } | null;
};

/** TrailRow ties a settled order back to the run that earned it. */
export type TrailRow = { run_id: string | null; order_id: string | null };

export type Figures = {
  uplift: number;
  upliftEarned: number;
  discountGiven: number;
  settledCount: number;
  settledValue: number;
  refundedCount: number;
  margin: number;
  marginPct: number;
  pricedCount: number;
  attachRate: number;
  attachedCount: number;
  offersPriced: number;
  ledgerNet: number;
  ledgerDifference: number;
  reconciled: boolean;
  runByOrder: Record<string, string>;
  salesOverTime: Point[];
  topProducts: ProductTotal[];
  lowStock: ProductRow[];
};

const settled = (status: string) => status.startsWith("fulfilled");

function day(iso: string): string {
  return new Date(iso).toLocaleDateString("en-IN", {
    day: "2-digit",
    month: "short",
  });
}

/**
 * summarize reduces the raw rows to the figures the operations page shows.
 * An order whose product carries no cost floor is left out of the margin
 * rather than counted at a guessed cost. The wallet movements are used to
 * reconcile the sales figures against the money that actually moved, so a
 * disagreement is visible rather than hidden behind a matching total.
 */
export function summarize(
  orders: OrderRow[],
  products: ProductRow[],
  revenue: RevenueRow[],
  ledger: LedgerRow[] = [],
  offers: OfferRow[] = [],
  trail: TrailRow[] = [],
): Figures {
  const productById = new Map(products.map((product) => [product.id, product]));
  const fulfilled = orders.filter((order) => settled(order.status));
  const refunded = orders.filter((order) =>
    order.status.startsWith("refunded"),
  );

  const priced = fulfilled.filter(
    (order) => (productById.get(order.product_id)?.cost_paise ?? 0) > 0,
  );
  const margin = priced.reduce((sum, order) => {
    const cost =
      (productById.get(order.product_id)?.cost_paise ?? 0) * order.qty;
    return sum + (order.amount_paise - cost);
  }, 0);
  const pricedValue = priced.reduce(
    (sum, order) => sum + order.amount_paise,
    0,
  );

  const perDay = new Map<string, number>();
  for (const order of [...fulfilled].sort((a, b) =>
    a.created_at.localeCompare(b.created_at),
  )) {
    const label = day(order.created_at);
    perDay.set(label, (perDay.get(label) ?? 0) + order.amount_paise);
  }

  const perProduct = new Map<string, ProductTotal>();
  for (const order of fulfilled) {
    const name = productById.get(order.product_id)?.name ?? "Unknown product";
    const entry = perProduct.get(order.product_id) ?? {
      name,
      count: 0,
      value: 0,
    };
    entry.count += order.qty;
    entry.value += order.amount_paise;
    perProduct.set(order.product_id, entry);
  }

  const settledValue = fulfilled.reduce(
    (sum, order) => sum + order.amount_paise,
    0,
  );
  const debits = ledger.filter((row) => row.entry_type === "purchase_debit");
  // Only movements for orders in this same window are compared. Reading a wider
  // slice of the ledger than of the orders would otherwise show a disagreement
  // that is only a difference in how much was read.
  const known = new Set(orders.map((order) => order.id));
  const inWindow = debits.filter(
    (row) => row.order_id !== null && known.has(row.order_id),
  );
  const ledgerNet = inWindow.reduce((sum, row) => sum + row.amount_paise, 0);
  const refundedIds = new Set(refunded.map((order) => order.id));
  const debitedButRefunded = inWindow
    .filter((row) => row.order_id !== null && refundedIds.has(row.order_id))
    .reduce((sum, row) => sum + row.amount_paise, 0);

  const attachedCount = offers.filter(
    (offer) => (offer.payload?.bundle_name ?? "").trim() !== "",
  ).length;

  const runByOrder: Record<string, string> = {};
  for (const row of trail) {
    if (row.order_id && row.run_id) {
      runByOrder[row.order_id] = row.run_id;
    }
  }

  return {
    uplift: revenue.reduce((sum, row) => sum + (row.uplift_paise ?? 0), 0),
    // Uplift and discount are reported separately on purpose. A single net figure
    // cancels a funded discount against an upsell and hides both.
    upliftEarned: revenue.reduce(
      (sum, row) => sum + Math.max(row.uplift_paise ?? 0, 0),
      0,
    ),
    discountGiven: revenue.reduce(
      (sum, row) => sum + Math.max(-(row.uplift_paise ?? 0), 0),
      0,
    ),
    settledCount: fulfilled.length,
    settledValue,
    refundedCount: refunded.length,
    margin,
    marginPct: pricedValue > 0 ? Math.round((margin / pricedValue) * 100) : 0,
    pricedCount: priced.length,
    attachRate:
      offers.length > 0 ? Math.round((attachedCount / offers.length) * 100) : 0,
    attachedCount,
    offersPriced: offers.length,
    ledgerNet,
    ledgerDifference: ledgerNet - debitedButRefunded - settledValue,
    reconciled: ledgerNet - debitedButRefunded === settledValue,
    runByOrder,
    salesOverTime: [...perDay.entries()]
      .slice(-10)
      .map(([label, value]) => ({ label, value })),
    topProducts: [...perProduct.values()]
      .sort((a, b) => b.value - a.value)
      .slice(0, 6),
    lowStock: [...products].sort((a, b) => a.stock - b.stock).slice(0, 6),
  };
}
