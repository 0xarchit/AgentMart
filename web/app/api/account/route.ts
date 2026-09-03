// Authenticated account read endpoint for dashboard clients.
import { serverFault } from "@/lib/errors";
import { createClient } from "@/lib/supabase/server";
import { NextResponse } from "next/server";

export async function GET() {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) return NextResponse.json({ error: "authentication required" }, { status: 401 });
  const { data, error } = await supabase.from("accounts").select("id,email,wallet_balance_paise,spend_limit_paise,created_at").eq("id", user.id).maybeSingle();
  if (error) return NextResponse.json({ error: serverFault("account read", error) }, { status: 502 });
  if (!data) return NextResponse.json({ error: "account not found" }, { status: 404 });
  return NextResponse.json(data);
}
