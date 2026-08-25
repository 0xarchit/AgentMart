import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";
import Link from "next/link";

const money = (paise: number) => `₹${(paise / 100).toLocaleString("en-IN", { maximumFractionDigits: 0 })}`;

export default async function AdminPage() {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) redirect("/login");
  const admins = (process.env.ADMIN_EMAILS ?? "").split(",").map((email) => email.trim().toLowerCase()).filter(Boolean);
  if (admins.length > 0 && !admins.includes((user.email ?? "").toLowerCase())) redirect("/dashboard");
  const [revenueResult, ordersResult, auditResult] = await Promise.all([
    supabase.from("merchant_revenue").select("order_id,base_amount_paise,final_amount_paise,uplift_paise,credited_at").order("credited_at", { ascending: false }).limit(100),
    supabase.from("orders").select("id,account_id,amount_paise,status,created_at").order("created_at", { ascending: false }).limit(100),
    supabase.from("audit_log").select("id,account_id,actor,action,reason,created_at").order("created_at", { ascending: false }).limit(100),
  ]);
  const revenue = revenueResult.data ?? [];
  const orders = ordersResult.data ?? [];
  const uplift = revenue.reduce((sum, row) => sum + (row.uplift_paise ?? 0), 0);
  return <main className="min-h-screen bg-paper px-6 py-10"><div className="mx-auto max-w-6xl">
    <nav className="flex items-center justify-between border-b border-ink/10 pb-5"><Link className="font-semibold text-moss" href="/">AgentMart Store</Link><div className="flex gap-4 text-sm"><Link href="/dashboard">User portal</Link><Link href="/admin" className="font-semibold text-moss">Admin</Link></div></nav>
    <div className="mt-8"><p className="text-xs font-semibold uppercase tracking-[0.18em] text-moss">Platform operations</p><h1 className="mt-2 text-4xl font-semibold">Admin revenue portal</h1><p className="mt-2 text-sm text-ink/60">Merchant uplift, fulfillment activity, and agent audit visibility.</p></div>
    <div className="mt-8 grid gap-4 md:grid-cols-3"><section className="border border-ink/10 bg-white p-5"><p className="text-sm text-ink/60">Merchant uplift</p><p className="mt-3 text-3xl font-semibold">{money(uplift)}</p></section><section className="border border-ink/10 bg-white p-5"><p className="text-sm text-ink/60">Orders</p><p className="mt-3 text-3xl font-semibold">{orders.length}</p></section><section className="border border-ink/10 bg-white p-5"><p className="text-sm text-ink/60">Audit events</p><p className="mt-3 text-3xl font-semibold">{auditResult.data?.length ?? 0}</p></section></div>
    <section className="mt-8 border border-ink/10 bg-white p-6"><h2 className="text-xl font-semibold">Revenue ledger</h2><div className="mt-4 divide-y divide-ink/10">{revenue.length === 0 ? <p className="py-4 text-sm text-ink/50">No revenue records yet.</p> : revenue.slice(0, 20).map((row) => <div key={row.order_id} className="flex items-center justify-between gap-4 py-3 text-sm"><span>Order {row.order_id.slice(0, 8)}</span><span className="font-semibold text-moss">+{money(row.uplift_paise)}</span></div>)}</div></section>
  </div></main>;
}
