// Creates authenticated Razorpay Checkout orders for wallet top-ups.
import { createRazorpayOrder } from "@/lib/razorpay";
import { createClient } from "@/lib/supabase/server";
import { NextResponse } from "next/server";

const MIN_TOPUP_PAISE = 10_000;
const MAX_TOPUP_PAISE = 10_000_000;

export const runtime = "nodejs";

export async function POST(request: Request) {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) return NextResponse.json({ error: "authentication required" }, { status: 401 });
  let amountPaise: number;
  try {
    const payload = await request.json();
    amountPaise = Number(payload.amount_paise);
  } catch {
    return NextResponse.json({ error: "invalid JSON" }, { status: 400 });
  }
  if (!Number.isInteger(amountPaise) || amountPaise < MIN_TOPUP_PAISE || amountPaise > MAX_TOPUP_PAISE) {
    return NextResponse.json({ error: "top-up amount must be between ₹100 and ₹100,000" }, { status: 400 });
  }
  const receipt = `topup_${user.id.replaceAll("-", "").slice(0, 12)}_${Date.now().toString(36)}`;
  try {
    const order = await createRazorpayOrder(amountPaise, receipt, { account_id: user.id, purpose: "wallet_topup" });
    return NextResponse.json({ order_id: order.id, amount_paise: order.amount, currency: order.currency, key_id: process.env.RAZORPAY_KEY_ID, account_id: user.id });
  } catch (error) {
    return NextResponse.json({ error: error instanceof Error ? error.message : "order creation failed" }, { status: 502 });
  }
}
