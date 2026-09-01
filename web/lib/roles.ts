// Reads the signed-in account's type. Every failure path resolves to a customer,
// so a missing row, a refused read or an unexpected value can never open the
// operator view.
import { createClient } from "./supabase/server";

export type AccountRole = "customer" | "admin";

export type Identity = {
  userId: string;
  email: string;
  role: AccountRole;
};

export async function currentIdentity(): Promise<Identity | null> {
  const supabase = await createClient();
  const {
    data: { user },
  } = await supabase.auth.getUser();
  if (!user) return null;

  const { data, error } = await supabase
    .from("accounts")
    .select("account_type")
    .eq("id", user.id)
    .maybeSingle();

  const role: AccountRole =
    !error && data?.account_type === "admin" ? "admin" : "customer";
  return { userId: user.id, email: user.email ?? "", role };
}
