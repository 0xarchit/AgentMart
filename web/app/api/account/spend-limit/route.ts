// Authenticated spend-limit update endpoint.
import { serverFault } from "@/lib/errors";
import { createAdminClient } from "@/lib/supabase/admin";
import { createClient } from "@/lib/supabase/server";
import { spendLimitAction } from "@/lib/words";
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
  // Read the standing limit first. This is the number the trail has to record
  // alongside the new one: a row saying only what the ceiling became cannot tell
  // anyone whether the agent's authority was widened or narrowed.
  const { data: before, error: beforeError } = await supabase.from("accounts").select("spend_limit_paise").eq("id", user.id).single();
  if (beforeError) return NextResponse.json({ error: serverFault("spend limit read", beforeError) }, { status: 502 });
  const previous = before.spend_limit_paise as number;
  const { data, error } = await supabase.from("accounts").update({ spend_limit_paise: value }).eq("id", user.id).select("id,spend_limit_paise").single();
  if (error) return NextResponse.json({ error: serverFault("spend limit update", error) }, { status: 502 });
  // This ceiling is what the agents may spend without asking, so moving it is a
  // change of authority and belongs on the trail beside the purchases it governs.
  // audit_log grants no insert to a signed-in caller, so the row goes through the
  // service role, after the caller's own session proved who they are.
  const { error: trailError } = await createAdminClient().from("audit_log").insert({
    account_id: user.id,
    actor: "account_owner",
    action: spendLimitAction(previous, value),
    reason: "the account owner set the amount the agents may spend without asking",
    payload: { from_paise: previous, to_paise: value },
  });
  if (trailError) {
    // Fail closed, as every other money boundary here does: an unrecorded change
    // to spending authority is put back rather than kept. If putting it back also
    // fails, say so plainly instead of reporting a clean update.
    const { error: revertError } = await supabase.from("accounts").update({ spend_limit_paise: previous }).eq("id", user.id);
    if (revertError) {
      serverFault("spend limit revert", revertError);
      return NextResponse.json({ error: "The limit was changed but could not be recorded, and could not be put back. Check the dashboard before shopping." }, { status: 502 });
    }
    return NextResponse.json({ error: serverFault("spend limit audit", trailError) }, { status: 502 });
  }
  return NextResponse.json(data);
}
