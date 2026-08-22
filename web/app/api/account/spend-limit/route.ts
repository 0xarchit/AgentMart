// Authenticated spend-limit update endpoint.
import { createClient } from "@/lib/supabase/server";
import { NextResponse } from "next/server";

const MAX_SPEND_LIMIT_PAISE = 10_000_000;

export async function PATCH(request: Request) {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) return NextResponse.json({ error: "authentication required" }, { status: 401 });
  let payload: { spend_limit_paise?: unknown };
  try {
    payload = await request.json();
  } catch {
    return NextResponse.json({ error: "invalid JSON" }, { status: 400 });
  }
  const value = typeof payload.spend_limit_paise === "number" ? payload.spend_limit_paise : Number(payload.spend_limit_paise);
  if (!Number.isInteger(value) || value <= 0 || value > MAX_SPEND_LIMIT_PAISE) {
    return NextResponse.json({ error: "spend limit must be a positive integer within the allowed maximum" }, { status: 400 });
  }
  const { data, error } = await supabase.from("accounts").update({ spend_limit_paise: value }).eq("id", user.id).select("id,spend_limit_paise").single();
  if (error) return NextResponse.json({ error: error.message }, { status: 502 });
  return NextResponse.json(data);
}
