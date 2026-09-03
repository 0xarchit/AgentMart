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

export type RazorpayPayment = {
  id: string;
  order_id: string;
  amount: number;
  status: string;
  notes?: { account_id?: string };
};

// Reads one payment back from the gateway. A browser can hand over an order id, a
// payment id and a valid signature for all three, but only the gateway can say what
// was actually captured and which account the top-up was opened for.
export async function fetchRazorpayPayment(paymentID: string): Promise<RazorpayPayment> {
  const { keyID, keySecret } = credentials();
  const response = await fetch(`https://api.razorpay.com/v1/payments/${encodeURIComponent(paymentID)}`, {
    headers: { Authorization: `Basic ${Buffer.from(`${keyID}:${keySecret}`).toString("base64")}` },
    cache: "no-store",
  });
  if (!response.ok) throw new Error(`Razorpay payment lookup failed with status ${response.status}`);
  return response.json();
}

export type TopUpRefusal = { status: number; error: string };

// Decides whether a verified Checkout callback may credit a wallet, returning the
// refusal or null to proceed. A valid signature proves the gateway issued that
// order and payment pair. It carries none of the facts below, and every one of them
// decides money.
export function refuseTopUp(payment: RazorpayPayment, orderID: string, accountID: string): TopUpRefusal | null {
  if (payment.order_id !== orderID) return { status: 409, error: "payment does not belong to that order" };
  if (payment.status !== "captured") return { status: 409, error: "payment is not captured" };
  if (!Number.isInteger(payment.amount) || payment.amount <= 0) return { status: 409, error: "payment amount is not a positive whole number of paise" };
  // The signed values are visible in the browser that paid them, so crediting
  // whoever presents them rather than whoever the order was opened for would let a
  // single payment fund two different wallets.
  if (!payment.notes?.account_id || payment.notes.account_id !== accountID) return { status: 403, error: "payment belongs to another account" };
  return null;
}

export function verifyWebhookSignature(body: string, signature: string): boolean {
  const webhookSecret = process.env.RAZORPAY_WEBHOOK_SECRET;
  if (!webhookSecret) throw new Error("Razorpay webhook secret is not configured");
  const expected = crypto.createHmac("sha256", webhookSecret).update(body).digest("hex");
  const expectedBytes = Buffer.from(expected);
  const signatureBytes = Buffer.from(signature);
  return expectedBytes.length === signatureBytes.length && crypto.timingSafeEqual(expectedBytes, signatureBytes);
}
