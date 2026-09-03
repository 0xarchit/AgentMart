// Credits a wallet from the browser Checkout callback, verified before it counts.
// The webhook writes the same credit under the same idempotency key, so the two
// paths race harmlessly: whichever arrives first credits, and the other is told the
// payment was already applied. Having both matters because a browser callback can
// be lost when the tab closes, and a webhook can be delayed.
import { fetchRazorpayPayment, refuseTopUp, verifyCheckoutSignature } from "@/lib/razorpay";
import { createAdminClient } from "@/lib/supabase/admin";
import { createClient } from "@/lib/supabase/server";
import { NextResponse } from "next/server";

export const runtime = "nodejs";

export async function POST(request: Request) {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) return NextResponse.json({ error: "authentication required" }, { status: 401 });

  let orderID = "";
  let paymentID = "";
  let signature = "";
  try {
    const payload = await request.json();
    orderID = String(payload.razorpay_order_id ?? "");
    paymentID = String(payload.razorpay_payment_id ?? "");
    signature = String(payload.razorpay_signature ?? "");
  } catch {
    return NextResponse.json({ error: "invalid JSON" }, { status: 400 });
  }
  if (!orderID || !paymentID || !signature) {
    return NextResponse.json({ error: "order id, payment id, and signature are required" }, { status: 400 });
  }

  try {
    if (!verifyCheckoutSignature(orderID, paymentID, signature)) {
      return NextResponse.json({ error: "invalid signature" }, { status: 401 });
    }
  } catch (error) {
    return NextResponse.json({ error: error instanceof Error ? error.message : "signature verification failed" }, { status: 500 });
  }

  let payment;
  try {
    payment = await fetchRazorpayPayment(paymentID);
  } catch (error) {
    return NextResponse.json({ error: error instanceof Error ? error.message : "payment lookup failed" }, { status: 502 });
  }
  const refusal = refuseTopUp(payment, orderID, user.id);
  if (refusal) return NextResponse.json({ error: refusal.error }, { status: refusal.status });

  const admin = createAdminClient();
  const { data, error } = await admin.rpc("credit_wallet_topup", {
    p_account_id: user.id,
    p_amount_paise: payment.amount,
    p_idempotency_key: `razorpay_payment:${payment.id}`,
    p_razorpay_order_id: payment.order_id,
    p_razorpay_payment_id: payment.id,
  });
  if (error) return NextResponse.json({ error: error.message }, { status: 409 });
  const outcome = data as { approved?: boolean; duplicate?: boolean; balance_paise?: number; reason?: string } | null;
  if (outcome?.approved === false) {
    return NextResponse.json({ error: outcome.reason ?? "top-up was refused" }, { status: 409 });
  }
  return NextResponse.json({
    credited: true,
    duplicate: outcome?.duplicate === true,
    amount_paise: payment.amount,
    balance_paise: outcome?.balance_paise ?? null,
  });
}
