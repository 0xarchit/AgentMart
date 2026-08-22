// Trusted Supabase client for server-only wallet mutations.
import { createClient } from "@supabase/supabase-js";

export function createAdminClient() {
  const url = process.env.SUPABASE_URL;
  const secretKey = process.env.SUPABASE_SECRET_KEY;
  if (!url || !secretKey) throw new Error("trusted Supabase configuration is missing");
  return createClient(url, secretKey, { auth: { persistSession: false, autoRefreshToken: false } });
}
