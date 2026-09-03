// Signature verification tests for the Checkout boundary.
import crypto from "node:crypto";
import { describe, expect, it } from "vitest";
import { refuseTopUp, verifyCheckoutSignature, verifyWebhookSignature, type RazorpayPayment } from "./razorpay";

describe("verifyCheckoutSignature", () => {
  it("accepts a valid Checkout signature", () => {
    process.env.RAZORPAY_KEY_SECRET = "secret";
    const signature = crypto.createHmac("sha256", "secret").update("order|payment").digest("hex");
    expect(verifyCheckoutSignature("order", "payment", signature)).toBe(true);
  });

  it("rejects a changed signature", () => {
    process.env.RAZORPAY_KEY_SECRET = "secret";
    expect(verifyCheckoutSignature("order", "payment", "bad")).toBe(false);
  });
});

describe("verifyWebhookSignature", () => {
  it("accepts a valid raw-body signature", () => {
    process.env.RAZORPAY_WEBHOOK_SECRET = "webhook";
    const signature = crypto.createHmac("sha256", "webhook").update("body").digest("hex");
    expect(verifyWebhookSignature("body", signature)).toBe(true);
  });
});

describe("refuseTopUp", () => {
  const captured: RazorpayPayment = {
    id: "pay_1",
    order_id: "order_1",
    amount: 100_000,
    status: "captured",
    notes: { account_id: "account-a" },
  };

  it("allows a captured payment for the account that opened the order", () => {
    expect(refuseTopUp(captured, "order_1", "account-a")).toBeNull();
  });

  it("refuses a payment the gateway has not captured", () => {
    expect(refuseTopUp({ ...captured, status: "authorized" }, "order_1", "account-a")).toEqual({
      status: 409,
      error: "payment is not captured",
    });
  });

  // The one that would cost real money: the signed triple is visible in the browser
  // that paid it, so presenting someone else's must not credit the presenter.
  it("refuses to credit an account the payment was not opened for", () => {
    expect(refuseTopUp(captured, "order_1", "account-b")?.status).toBe(403);
  });

  it("refuses a payment with no account on it", () => {
    expect(refuseTopUp({ ...captured, notes: {} }, "order_1", "account-a")?.status).toBe(403);
  });

  it("refuses a payment that belongs to a different order", () => {
    expect(refuseTopUp(captured, "order_2", "account-a")?.status).toBe(409);
  });

  it("refuses an amount that is not a positive whole number of paise", () => {
    expect(refuseTopUp({ ...captured, amount: 0 }, "order_1", "account-a")?.status).toBe(409);
    expect(refuseTopUp({ ...captured, amount: 12.5 }, "order_1", "account-a")?.status).toBe(409);
  });
});
