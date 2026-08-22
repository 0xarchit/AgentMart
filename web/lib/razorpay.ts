// Server-side Razorpay Checkout order and signature helpers.
import crypto from "node:crypto";

export type RazorpayOrder = {
  id: string;
  amount: number;
  currency: string;
  receipt: string;
  status: string;
};

function credentials() {
  const keyID = process.env.RAZORPAY_KEY_ID;
  const keySecret = process.env.RAZORPAY_KEY_SECRET;
  if (!keyID || !keySecret) throw new Error("Razorpay credentials are not configured");
  return { keyID, keySecret };
}

function keySecret() {
  const value = process.env.RAZORPAY_KEY_SECRET;
  if (!value) throw new Error("Razorpay key secret is not configured");
  return value;
}

export async function createRazorpayOrder(amountPaise: number, receipt: string, notes: Record<string, string>): Promise<RazorpayOrder> {
  if (!Number.isInteger(amountPaise) || amountPaise <= 0) throw new Error("amount must be a positive integer");
  const { keyID, keySecret } = credentials();
  const response = await fetch("https://api.razorpay.com/v1/orders", {
    method: "POST",
    headers: {
      Authorization: `Basic ${Buffer.from(`${keyID}:${keySecret}`).toString("base64")}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ amount: amountPaise, currency: "INR", receipt, payment_capture: 1, notes }),
    cache: "no-store",
  });
  if (!response.ok) throw new Error(`Razorpay order creation failed with status ${response.status}`);
  return response.json();
}

export function verifyCheckoutSignature(orderID: string, paymentID: string, signature: string): boolean {
  const expected = crypto.createHmac("sha256", keySecret()).update(`${orderID}|${paymentID}`).digest("hex");
  const expectedBytes = Buffer.from(expected);
  const signatureBytes = Buffer.from(signature);
  return expectedBytes.length === signatureBytes.length && crypto.timingSafeEqual(expectedBytes, signatureBytes);
}

export function verifyWebhookSignature(body: string, signature: string): boolean {
  const webhookSecret = process.env.RAZORPAY_WEBHOOK_SECRET;
  if (!webhookSecret) throw new Error("Razorpay webhook secret is not configured");
  const expected = crypto.createHmac("sha256", webhookSecret).update(body).digest("hex");
  const expectedBytes = Buffer.from(expected);
  const signatureBytes = Buffer.from(signature);
  return expectedBytes.length === signatureBytes.length && crypto.timingSafeEqual(expectedBytes, signatureBytes);
}
