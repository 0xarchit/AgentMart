// Proves each operations figure comes from rows, and that a missing cost floor
// is excluded from the margin instead of guessed.
import { describe, expect, it } from "vitest";
import { summarize, type OrderRow, type ProductRow } from "./metrics";

const products: ProductRow[] = [
  { id: "p1", name: "Trimmer", stock: 12, cost_paise: 60_000, price_paise: 100_000 },
  { id: "p2", name: "Cream", stock: 2, cost_paise: 20_000, price_paise: 45_000 },
  { id: "p3", name: "Unpriced", stock: 40, cost_paise: 0, price_paise: 10_000 },
];

const order = (over: Partial<OrderRow>): OrderRow => ({
  id: "o",
  product_id: "p1",
  qty: 1,
  amount_paise: 100_000,
  status: "fulfilled_via_wallet",
  created_at: "2026-08-25T10:00:00Z",
  ...over,
});

describe("summarize", () => {
  it("counts only settled orders in value and refunds separately", () => {
    const figures = summarize(
      [
        order({ id: "a" }),
        order({ id: "b", product_id: "p2", amount_paise: 45_000 }),
        order({ id: "c", status: "refunded_via_wallet" }),
      ],
      products,
      [],
    );

    expect(figures.settledCount).toBe(2);
    expect(figures.settledValue).toBe(145_000);
    expect(figures.refundedCount).toBe(1);
  });

  it("leaves an order without a cost floor out of the margin", () => {
    const figures = summarize(
      [order({ id: "a" }), order({ id: "b", product_id: "p3", amount_paise: 10_000 })],
      products,
      [],
    );

    expect(figures.pricedCount).toBe(1);
    expect(figures.margin).toBe(40_000);
    expect(figures.marginPct).toBe(40);
  });

  it("adds uplift from credited rows and nothing else", () => {
    const figures = summarize([], products, [
      {
        order_id: "a",
        base_amount_paise: 100_000,
        final_amount_paise: 112_000,
        uplift_paise: 12_000,
        credited_at: "2026-08-25T10:00:00Z",
      },
    ]);

    expect(figures.uplift).toBe(12_000);
    expect(figures.settledValue).toBe(0);
  });

  it("groups sales by day in order and ranks products by value", () => {
    const figures = summarize(
      [
        order({ id: "a", created_at: "2026-08-25T10:00:00Z" }),
        order({ id: "b", created_at: "2026-08-26T10:00:00Z", amount_paise: 30_000 }),
        order({
          id: "c",
          created_at: "2026-08-26T18:00:00Z",
          product_id: "p2",
          amount_paise: 45_000,
        }),
      ],
      products,
      [],
    );

    expect(figures.salesOverTime.map((point) => point.value)).toEqual([
      100_000, 75_000,
    ]);
    expect(figures.topProducts[0]).toEqual({
      name: "Trimmer",
      count: 2,
      value: 130_000,
    });
  });

  it("puts the thinnest stock first", () => {
    const figures = summarize([], products, []);

    expect(figures.lowStock[0].name).toBe("Cream");
  });
});
