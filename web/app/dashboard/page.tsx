// Protected dashboard landing page.
import { signOut } from "@/app/auth/actions";
import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";

export default async function DashboardPage() {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) redirect("/login");
  return (
    <main className="min-h-screen bg-paper px-6 py-10">
      <div className="mx-auto max-w-5xl">
        <div className="flex items-start justify-between border-b border-ink/10 pb-6">
          <div>
            <p className="text-xs font-semibold uppercase tracking-[0.18em] text-moss">Authenticated workspace</p>
            <h1 className="mt-2 text-3xl font-semibold">Operations dashboard</h1>
            <p className="mt-2 text-sm text-ink/60">{user.email}</p>
          </div>
          <form action={signOut}><button className="border border-ink/20 px-4 py-2 text-sm font-medium" type="submit">Sign out</button></form>
        </div>
        <div className="mt-8 grid gap-4 md:grid-cols-3">
          {["Wallet balance", "Open orders", "Merchant uplift"].map((label) => <section key={label} className="border border-ink/10 bg-white p-5"><p className="text-sm text-ink/60">{label}</p><p className="mt-3 text-2xl font-semibold">Not connected</p><p className="mt-2 text-xs text-ink/50">This view is wired in the next wallet milestone.</p></section>)}
        </div>
      </div>
    </main>
  );
}
