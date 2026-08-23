// Protected dashboard landing page.
import { signOut } from "@/app/auth/actions";
import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";
import { TopUpButton } from "@/app/dashboard/topup-button";
import { LinkTelegram } from "@/app/dashboard/link-telegram";

type Account = {
  wallet_balance_paise: number;
  spend_limit_paise: number;
};

type Order = {
  id: string;
  amount_paise: number;
  status: string;
  created_at: string;
};

type LedgerEntry = {
  id: string;
  entry_type: string;
  amount_paise: number;
  balance_after_paise: number;
  created_at: string;
};

type Revenue = {
  order_id: string;
  base_amount_paise: number;
  final_amount_paise: number;
  uplift_paise: number;
  credited_at: string;
};

function formatRupees(paise: number): string {
  return `₹${(paise / 100).toLocaleString("en-IN", { maximumFractionDigits: 0 })}`;
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat("en-IN", { dateStyle: "medium" }).format(new Date(value));
}

export default async function DashboardPage() {
  const supabase = await createClient();
  const { data: { user } } = await supabase.auth.getUser();
  if (!user) redirect("/login");
  const [accountResult, ordersResult, ledgerResult, revenueResult] = await Promise.all([
    supabase.from("accounts").select("wallet_balance_paise,spend_limit_paise").eq("id", user.id).maybeSingle(),
    supabase.from("orders").select("id,amount_paise,status,created_at").eq("account_id", user.id).order("created_at", { ascending: false }).limit(5),
    supabase.from("wallet_ledger").select("id,entry_type,amount_paise,balance_after_paise,created_at").eq("account_id", user.id).order("created_at", { ascending: false }).limit(5),
    supabase.from("merchant_revenue").select("order_id,base_amount_paise,final_amount_paise,uplift_paise,credited_at").order("credited_at", { ascending: false }).limit(100),
  ]);
  const account = accountResult.data as Account | null;
  const orders = (ordersResult.data ?? []) as Order[];
  const ledger = (ledgerResult.data ?? []) as LedgerEntry[];
  const revenue = (revenueResult.data ?? []) as Revenue[];
  const upliftTotal = revenue.reduce((sum, row) => sum + row.uplift_paise, 0);
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
          <section className="border border-ink/10 bg-white p-5"><p className="text-sm text-ink/60">Wallet balance</p><p className="mt-3 text-2xl font-semibold">{formatRupees(account?.wallet_balance_paise ?? 0)}</p><p className="mt-2 text-xs text-ink/50">Spend limit {formatRupees(account?.spend_limit_paise ?? 0)}</p></section>
          <section className="border border-ink/10 bg-white p-5"><p className="text-sm text-ink/60">Recent orders</p><p className="mt-3 text-2xl font-semibold">{orders.length}</p><p className="mt-2 text-xs text-ink/50">Account-scoped by policy</p></section>
          <section className="border border-ink/10 bg-white p-5"><p className="text-sm text-ink/60">Merchant uplift</p><p className="mt-3 text-2xl font-semibold">{formatRupees(upliftTotal)}</p><p className="mt-2 text-xs text-ink/50">Across fulfilled revenue records</p></section>
        </div>
        <div className="mt-8 grid gap-8 lg:grid-cols-2">
          <section className="border border-ink/10 bg-white p-5"><h2 className="text-lg font-semibold">Recent orders</h2><div className="mt-4 divide-y divide-ink/10">{orders.length === 0 ? <p className="py-4 text-sm text-ink/50">No orders yet.</p> : orders.map((order) => <div key={order.id} className="flex items-center justify-between gap-4 py-3 text-sm"><div><p className="font-medium">{order.id.slice(0, 8)}</p><p className="text-xs text-ink/50">{formatDate(order.created_at)}</p></div><div className="text-right"><p className="font-semibold">{formatRupees(order.amount_paise)}</p><p className="text-xs text-moss">{order.status}</p></div></div>)}</div></section>
          <section className="border border-ink/10 bg-white p-5"><h2 className="text-lg font-semibold">Wallet movements</h2><div className="mt-4 divide-y divide-ink/10">{ledger.length === 0 ? <p className="py-4 text-sm text-ink/50">No wallet movements yet.</p> : ledger.map((entry) => <div key={entry.id} className="flex items-center justify-between gap-4 py-3 text-sm"><div><p className="font-medium">{entry.entry_type}</p><p className="text-xs text-ink/50">{formatDate(entry.created_at)}</p></div><div className="text-right"><p className="font-semibold">{entry.amount_paise >= 0 ? "+" : ""}{formatRupees(entry.amount_paise)}</p><p className="text-xs text-ink/50">Balance {formatRupees(entry.balance_after_paise)}</p></div></div>)}</div></section>
          <section className="border border-ink/10 bg-white p-5"><h2 className="text-lg font-semibold">Merchant revenue</h2><div className="mt-4 divide-y divide-ink/10">{revenue.length === 0 ? <p className="py-4 text-sm text-ink/50">No fulfilled revenue yet.</p> : revenue.slice(0, 5).map((row) => <div key={row.order_id} className="py-3 text-sm"><div className="flex items-center justify-between gap-4"><div><p className="font-medium">Order {row.order_id.slice(0, 8)}</p><p className="text-xs text-ink/50">{formatDate(row.credited_at)}</p></div><p className="font-semibold text-moss">+{formatRupees(row.uplift_paise)}</p></div><p className="mt-1 text-xs text-ink/60">Base {formatRupees(row.base_amount_paise)} to final {formatRupees(row.final_amount_paise)}</p></div>)}</div></section>
        </div>
        <div className="mt-8"><TopUpButton /></div>
        <div className="mt-8"><LinkTelegram /></div>
      </div>
    </main>
  );
}
