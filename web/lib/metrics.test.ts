// Proves each operations figure comes from rows, and that a missing cost floor
// is excluded from the margin instead of guessed.
import { describe, expect, it } from "vitest";
import {
  summarize,
  type LedgerRow,
  type OfferRow,
  type OrderRow,
  type ProductRow,
} from "./metrics";

const products: ProductRow[] = [
  {
    id: "p1",
    name: "Trimmer",
    stock: 12,
    cost_paise: 60_000,
    price_paise: 100_000,
  },
  {
    id: "p2",
    name: "Cream",
    stock: 2,
    cost_paise: 20_000,
    price_paise: 45_000,
  },
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
      [
        order({ id: "a" }),
        order({ id: "b", product_id: "p3", amount_paise: 10_000 }),
      ],
      products,
      [],
    );

    expect(figures.pricedCount).toBe(1);
    expect(figures.margin).toBe(40_000);
    expect(figures.marginPct).toBe(40);
  });

  it("reports uplift earned and discount given separately, never netted", () => {
    const figures = summarize([], products, [
      {
        order_id: "a",
        base_amount_paise: 100_000,
        final_amount_paise: 112_000,
        uplift_paise: 12_000,
        credited_at: "2026-08-25T10:00:00Z",
      },
      {
        order_id: "b",
        base_amount_paise: 100_000,
        final_amount_paise: 88_000,
        uplift_paise: -12_000,
        credited_at: "2026-08-25T11:00:00Z",
      },
    ]);

    // Netted these cancel to zero, which would hide both the upsell and the
    // funded discount behind one meaningless figure.
    expect(figures.upliftEarned).toBe(12_000);
    expect(figures.discountGiven).toBe(12_000);
    expect(figures.uplift).toBe(0);
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
        order({
          id: "b",
          created_at: "2026-08-26T10:00:00Z",
          amount_paise: 30_000,
        }),
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

describe("the revenue scoreboard", () => {
  it("reconciles the settled value against the money that actually left wallets", () => {
    const orders = [
      order({ id: "a" }),
      order({ id: "b", product_id: "p2", amount_paise: 45_000 }),
    ];
    const ledger: LedgerRow[] = [
      { order_id: "a", entry_type: "purchase_debit", amount_paise: 100_000 },
      { order_id: "b", entry_type: "purchase_debit", amount_paise: 45_000 },
      { order_id: null, entry_type: "topup", amount_paise: 500_000 },
    ];
    const figures = summarize(orders, products, [], ledger);
    expect(figures.ledgerNet).toBe(145_000);
    expect(figures.ledgerDifference).toBe(0);
    expect(figures.reconciled).toBe(true);
  });

  it("still reconciles when an order was settled and then refunded", () => {
    const orders = [
      order({ id: "a" }),
      order({ id: "b", status: "refunded_via_wallet", amount_paise: 45_000 }),
    ];
    const ledger: LedgerRow[] = [
      { order_id: "a", entry_type: "purchase_debit", amount_paise: 100_000 },
      { order_id: "b", entry_type: "purchase_debit", amount_paise: 45_000 },
      { order_id: "b", entry_type: "purchase_refund", amount_paise: 45_000 },
    ];
    const figures = summarize(orders, products, [], ledger);
    expect(figures.settledValue).toBe(100_000);
    expect(figures.reconciled).toBe(true);
  });

  it("reports a disagreement instead of hiding it", () => {
    const ledger: LedgerRow[] = [
      { order_id: "a", entry_type: "purchase_debit", amount_paise: 90_000 },
    ];
    const figures = summarize([order({ id: "a" })], products, [], ledger);
    expect(figures.reconciled).toBe(false);
    expect(figures.ledgerDifference).toBe(-10_000);
  });

  it("takes the attach rate from offers that carried a partner product", () => {
    const offers: OfferRow[] = [
      { run_id: "r1", payload: { bundle_name: "Blade Oil" } },
      { run_id: "r1", payload: { bundle_name: "  " } },
      { run_id: "r2", payload: { bundle_name: "Travel Case" } },
      { run_id: "r3", payload: null },
    ];
    const figures = summarize([], products, [], [], offers);
    expect(figures.offersPriced).toBe(4);
    expect(figures.attachedCount).toBe(2);
    expect(figures.attachRate).toBe(50);
  });

  it("maps a settled order to the run that earned it", () => {
    const figures = summarize(
      [order({ id: "a" })],
      products,
      [],
      [],
      [],
      [
        { order_id: "a", run_id: "run-one" },
        { order_id: null, run_id: "run-two" },
        { order_id: "b", run_id: null },
      ],
    );
    expect(figures.runByOrder).toEqual({ a: "run-one" });
  });

  it("ignores a debit for an order outside the window being shown", () => {
    const ledger: LedgerRow[] = [
      { order_id: "a", entry_type: "purchase_debit", amount_paise: 100_000 },
      {
        order_id: "older",
        entry_type: "purchase_debit",
        amount_paise: 999_000,
      },
    ];
    const figures = summarize([order({ id: "a" })], products, [], ledger);
    expect(figures.ledgerNet).toBe(100_000);
    expect(figures.reconciled).toBe(true);
  });

  it("shows no attach rate rather than a false zero when nothing was priced", () => {
    const figures = summarize([], products, []);
    expect(figures.offersPriced).toBe(0);
    expect(figures.attachRate).toBe(0);
    expect(figures.reconciled).toBe(true);
  });
});
