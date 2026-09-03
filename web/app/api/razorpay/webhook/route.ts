// Verifies Razorpay webhooks before crediting the internal wallet.
import { serverFault } from "@/lib/errors";
import { refuseWalletCredit, verifyWebhookSignature } from "@/lib/razorpay";
import { createAdminClient } from "@/lib/supabase/admin";
import { NextResponse } from "next/server";

type CapturedPayment = {
  id: string;
  order_id: string;
  amount: number;
  status: string;
  notes?: {
    account_id?: string;
    purpose?: string;
    run_id?: string;
    order_id?: string;
  };
  error_description?: string;
  error_reason?: string;
};

type GatewayRefund = {
  id: string;
  payment_id: string;
  amount: number;
  status: string;
  notes?: { account_id?: string; run_id?: string; order_id?: string };
};

export const runtime = "nodejs";

export async function POST(request: Request) {
  const rawBody = await request.text();
  const signature = request.headers.get("x-razorpay-signature") ?? "";
  try {
    if (!verifyWebhookSignature(rawBody, signature))
      return NextResponse.json({ error: "invalid signature" }, { status: 401 });
  } catch (error) {
    return NextResponse.json(
      { error: serverFault("webhook signature", error) },
      { status: 500 },
    );
  }
  let event: {
    event?: string;
    payload?: {
      payment?: { entity?: CapturedPayment };
      refund?: { entity?: GatewayRefund };
    };
  };
  try {
    event = JSON.parse(rawBody);
  } catch {
    return NextResponse.json(
      { error: "invalid webhook JSON" },
      { status: 400 },
    );
  }

  // A failure the gateway reports is worth more than one we generate ourselves, so
  // it is recorded against the run that caused it instead of being discarded.
  if (
    event.event === "payment.failed" ||
    event.event === "refund.processed" ||
    event.event === "refund.failed"
  ) {
    return recordGatewayEvent(
      event.event,
      event.payload?.payment?.entity,
      event.payload?.refund?.entity,
    );
  }
  if (event.event !== "payment.captured")
    return NextResponse.json({ received: true, ignored: true });
  const payment = event.payload?.payment?.entity;
  const accountID = payment?.notes?.account_id;
  // The same decision the browser callback makes, so a payment either funds a
  // wallet on both paths or on neither. The account id is checked again here only
  // to narrow it for the call below.
  if (!payment || !accountID || refuseWalletCredit(payment)) {
    return NextResponse.json(
      { error: "incomplete captured payment" },
      { status: 400 },
    );
  }
  const admin = createAdminClient();
  const { error } = await admin.rpc("credit_wallet_topup", {
    p_account_id: accountID,
    p_amount_paise: payment.amount,
    p_idempotency_key: `razorpay_payment:${payment.id}`,
    p_razorpay_order_id: payment.order_id,
    p_razorpay_payment_id: payment.id,
  });
  if (error)
    return NextResponse.json(
      { error: serverFault("webhook wallet credit", error) },
      { status: 409 },
    );
  return NextResponse.json({ received: true });
}

// recordGatewayEvent writes a gateway reported outcome to the trail. It never
// moves money: a failed payment credits nothing, and a reversal was already
// credited internally before it was ever sent to the gateway.
async function recordGatewayEvent(
  name: string,
  payment?: CapturedPayment,
  refund?: GatewayRefund,
) {
  const entity = refund ?? payment;
  const accountID = entity?.notes?.account_id;
  if (!entity || !accountID)
    return NextResponse.json({ received: true, ignored: true });
  const reason =
    payment?.error_description ??
    payment?.error_reason ??
    (name === "refund.processed"
      ? "the gateway completed the reversal"
      : "the gateway reported an outcome");
  const admin = createAdminClient();
  const { error } = await admin.from("audit_log").insert({
    account_id: accountID,
    order_id: entity.notes?.order_id ?? null,
    run_id: entity.notes?.run_id ?? null,
    actor: "gateway",
    action: name.replace(".", "_"),
    reason,
    payload: {
      id: entity.id,
      amount_paise: entity.amount,
      status: entity.status,
    },
  });
  if (error)
    return NextResponse.json(
      { error: serverFault("gateway event record", error) },
      { status: 409 },
    );
  return NextResponse.json({ received: true, recorded: name });
}
