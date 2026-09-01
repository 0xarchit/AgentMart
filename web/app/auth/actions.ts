// Authentication server actions for dashboard access.
"use server";

import { createClient } from "@/lib/supabase/server";
import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";

export async function signIn(formData: FormData) {
  const supabase = await createClient();
  const { error } = await supabase.auth.signInWithPassword({ email: String(formData.get("email")), password: String(formData.get("password")) });
  if (error) redirect(`/login?error=${encodeURIComponent(error.message)}`);
  revalidatePath("/", "layout");
  redirect("/dashboard");
}

export async function signInWithGoogle() {
  const supabase = await createClient();
  const appURL = process.env.NEXT_PUBLIC_APP_URL ?? "http://localhost:3000";
  const { data, error } = await supabase.auth.signInWithOAuth({
    provider: "google",
    options: { redirectTo: `${appURL}/auth/callback?next=/dashboard` },
  });
  if (error) redirect(`/login?error=${encodeURIComponent(error.message)}`);
  if (data.url) redirect(data.url);
  redirect("/login?error=Google%20sign-in%20did%20not%20return%20a%20redirect");
}

// signInAsOperator uses the same provider but its own door and its own error
// destination, so a refused operator attempt never lands on the customer form.
// It grants nothing by itself: the operator view reads the account type.
export async function signInAsOperator() {
  const supabase = await createClient();
  const appURL = process.env.NEXT_PUBLIC_APP_URL ?? "http://localhost:3000";
  const { data, error } = await supabase.auth.signInWithOAuth({
    provider: "google",
    options: { redirectTo: `${appURL}/auth/callback?next=/admin` },
  });
  if (error) redirect(`/admin/login?error=${encodeURIComponent(error.message)}`);
  if (data.url) redirect(data.url);
  redirect("/admin/login?error=sign-in%20did%20not%20return%20a%20redirect");
}

// signOutToOperatorDoor ends the session and returns to the operator door rather
// than the storefront, so a person signed in with the wrong account can retry
// without leaving the page they were trying to reach.
export async function signOutToOperatorDoor() {
  const supabase = await createClient();
  await supabase.auth.signOut();
  revalidatePath("/", "layout");
  redirect("/admin/login");
}

export async function signUp(formData: FormData) {
  const supabase = await createClient();
  const { error } = await supabase.auth.signUp({ email: String(formData.get("email")), password: String(formData.get("password")) });
  if (error) redirect(`/login?error=${encodeURIComponent(error.message)}`);
  redirect("/login?registered=1");
}

export async function signOut() {
  const supabase = await createClient();
  await supabase.auth.signOut();
  revalidatePath("/", "layout");
  redirect("/");
}
