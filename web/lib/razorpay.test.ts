// Signature verification tests for the Checkout boundary.
import crypto from "node:crypto";
import { describe, expect, it } from "vitest";
import { verifyCheckoutSignature, verifyWebhookSignature } from "./razorpay";

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
