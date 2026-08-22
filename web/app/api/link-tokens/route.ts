// Generates short-lived single-use Telegram linking tokens.
import crypto from "node:crypto";
import { createAdminClient } from "@/lib/supabase/admin";
import { createClient } from "@/lib/supabase/server";
import { NextResponse } from "next/server";

export const runtime = "nodejs";

export async function POST() {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) return NextResponse.json({ error: "authentication required" }, { status: 401 });
  const token = crypto.randomBytes(24).toString("base64url");
  const expiresAt = new Date(Date.now() + 10 * 60 * 1000).toISOString();
  const admin = createAdminClient();
  const { error } = await admin.from("link_tokens").insert({ token, account_id: user.id, expires_at: expiresAt });
  if (error) return NextResponse.json({ error: error.message }, { status: 502 });
  return NextResponse.json({ token, expires_at: expiresAt });
}
