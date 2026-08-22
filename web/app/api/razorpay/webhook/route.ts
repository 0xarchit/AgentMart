// Verifies Razorpay webhooks before crediting the internal wallet.
import { verifyWebhookSignature } from "@/lib/razorpay";
import { createAdminClient } from "@/lib/supabase/admin";
import { NextResponse } from "next/server";

type CapturedPayment = {
  id: string;
  order_id: string;
  amount: number;
  status: string;
  notes?: { account_id?: string };
};

export const runtime = "nodejs";

export async function POST(request: Request) {
  const rawBody = await request.text();
  const signature = request.headers.get("x-razorpay-signature") ?? "";
  try {
    if (!verifyWebhookSignature(rawBody, signature)) return NextResponse.json({ error: "invalid signature" }, { status: 401 });
  } catch (error) {
    return NextResponse.json({ error: error instanceof Error ? error.message : "webhook verification failed" }, { status: 500 });
  }
  let event: { event?: string; payload?: { payment?: { entity?: CapturedPayment } } };
  try {
    event = JSON.parse(rawBody);
  } catch {
    return NextResponse.json({ error: "invalid webhook JSON" }, { status: 400 });
  }
  if (event.event !== "payment.captured") return NextResponse.json({ received: true, ignored: true });
  const payment = event.payload?.payment?.entity;
  const accountID = payment?.notes?.account_id;
  if (!payment || payment.status !== "captured" || !accountID || !payment.order_id || !Number.isInteger(payment.amount) || payment.amount <= 0) {
    return NextResponse.json({ error: "incomplete captured payment" }, { status: 400 });
  }
  const admin = createAdminClient();
  const { error } = await admin.rpc("credit_wallet_topup", {
    p_account_id: accountID,
    p_amount_paise: payment.amount,
    p_idempotency_key: `razorpay_payment:${payment.id}`,
    p_razorpay_order_id: payment.order_id,
    p_razorpay_payment_id: payment.id,
  });
  if (error) return NextResponse.json({ error: error.message }, { status: 409 });
  return NextResponse.json({ received: true });
}
