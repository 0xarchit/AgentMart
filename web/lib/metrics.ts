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

export type Figures = {
  uplift: number;
  settledCount: number;
  settledValue: number;
  refundedCount: number;
  margin: number;
  marginPct: number;
  pricedCount: number;
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
 * rather than counted at a guessed cost.
 */
export function summarize(
  orders: OrderRow[],
  products: ProductRow[],
  revenue: RevenueRow[],
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

  return {
    uplift: revenue.reduce((sum, row) => sum + (row.uplift_paise ?? 0), 0),
    settledCount: fulfilled.length,
    settledValue: fulfilled.reduce((sum, order) => sum + order.amount_paise, 0),
    refundedCount: refunded.length,
    margin,
    marginPct: pricedValue > 0 ? Math.round((margin / pricedValue) * 100) : 0,
    pricedCount: priced.length,
    salesOverTime: [...perDay.entries()]
      .slice(-10)
      .map(([label, value]) => ({ label, value })),
    topProducts: [...perProduct.values()]
      .sort((a, b) => b.value - a.value)
      .slice(0, 6),
    lowStock: [...products].sort((a, b) => a.stock - b.stock).slice(0, 6),
  };
}
