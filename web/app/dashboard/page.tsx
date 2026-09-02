// Protected dashboard landing page.
import { signOut } from "@/app/auth/actions";
import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";
import Link from "next/link";
import { TopUpButton } from "@/app/dashboard/topup-button";
import { LinkTelegram } from "@/app/dashboard/link-telegram";
import { SpendLimitEditor } from "@/app/dashboard/spend-limit-editor";
import { AuditTimeline, type AuditEvent } from "@/app/dashboard/audit-timeline";
import { Card, Rows, Stat, TopNav, money } from "@/app/ui";
import { plainWords } from "@/lib/words";

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

type Run = {
  run_id: string;
  request: string | null;
  product_name: string | null;
  outcome: string | null;
  final_amount_paise: number | null;
  started_at: string;
};

type Revenue = {
  order_id: string;
  base_amount_paise: number;
  final_amount_paise: number;
  uplift_paise: number;
  credited_at: string;
};

function formatDate(value: string): string {
  return new Intl.DateTimeFormat("en-IN", { dateStyle: "medium" }).format(
    new Date(value),
  );
}

export default async function DashboardPage() {
  const supabase = await createClient();
  const {
    data: { user },
  } = await supabase.auth.getUser();
  if (!user) redirect("/login");
  const [
    accountResult,
    ordersResult,
    orderCountResult,
    ledgerResult,
    revenueResult,
    auditResult,
    runsResult,
  ] = await Promise.all([
    supabase
      .from("accounts")
      .select("wallet_balance_paise,spend_limit_paise")
      .eq("id", user.id)
      .maybeSingle(),
    supabase
      .from("orders")
      .select("id,amount_paise,status,created_at")
      .eq("account_id", user.id)
      .order("created_at", { ascending: false })
      .limit(5),
    // Counted separately from the list above. Reading the length of a five row
    // page reports "5" forever once an account has bought six things.
    supabase
      .from("orders")
      .select("id", { count: "exact", head: true })
      .eq("account_id", user.id),
    supabase
      .from("wallet_ledger")
      .select("id,entry_type,amount_paise,balance_after_paise,created_at")
      .eq("account_id", user.id)
      .order("created_at", { ascending: false })
      .limit(5),
    supabase
      .from("merchant_revenue")
      .select(
        "order_id,base_amount_paise,final_amount_paise,uplift_paise,credited_at",
      )
      .order("credited_at", { ascending: false })
      .limit(100),
    supabase
      .from("audit_log")
      .select("id,order_id,actor,action,reason,created_at")
      .eq("account_id", user.id)
      .order("created_at", { ascending: false })
      .limit(100),
    supabase
      .from("run_summary")
      .select(
        "run_id,request,product_name,outcome,final_amount_paise,started_at",
      )
      .order("started_at", { ascending: false })
      .limit(5),
  ]);
  const account = accountResult.data as Account | null;
  const orders = (ordersResult.data ?? []) as Order[];
  const orderCount = orderCountResult.count ?? orders.length;
  const ledger = (ledgerResult.data ?? []) as LedgerEntry[];
  const revenue = (revenueResult.data ?? []) as Revenue[];
  const auditEvents = (auditResult.data ?? []) as AuditEvent[];
  const runs = (runsResult.data ?? []) as Run[];
  // Reported separately rather than netted, so a funded discount cannot cancel an
  // upsell and leave one figure that says nothing.
  const upliftEarned = revenue.reduce(
    (sum, row) => sum + Math.max(row.uplift_paise, 0),
    0,
  );
  const discountGiven = revenue.reduce(
    (sum, row) => sum + Math.max(-row.uplift_paise, 0),
    0,
  );
  return (
    <main className="min-h-screen bg-paper px-6 py-10">
      <div className="mx-auto max-w-5xl">
        <TopNav
          current="dashboard"
          action={
            <form action={signOut}>
              <button
                className="border border-ink/20 px-3 py-2 text-sm font-medium"
                type="submit"
              >
                Sign out
              </button>
            </form>
          }
        />
        <div className="mt-8 border-b border-ink/10 pb-6">
          <h1 className="text-3xl font-semibold">Your account</h1>
          <p className="mt-2 text-sm text-ink/70">
            Signed in as {user.email}
          </p>
        </div>
        <div className="mt-8 grid gap-4 md:grid-cols-3">
          <Stat
            label="Wallet balance"
            value={money(account?.wallet_balance_paise ?? 0)}
            basis={`Spend limit ${money(account?.spend_limit_paise ?? 0)}`}
          />
          <Stat
            label="Orders placed"
            value={String(orderCount)}
            basis="Only ever your own account"
          />
          <Stat
            label="Uplift earned"
            value={money(upliftEarned)}
            basis={`${revenue.length} credited row(s)`}
            tone="moss"
          />
          <Stat
            label="Discount given"
            value={money(discountGiven)}
            basis="Funded from loyalty entitlement"
            tone={discountGiven > 0 ? "coral" : "moss"}
          />
        </div>

        <div className="mt-8 border-b border-ink/10 pb-2">
          <h2 className="text-lg font-semibold">What you can do</h2>
          <p className="mt-1 text-sm text-ink/70">
            Add money, set the limit the agents may spend without asking, and
            connect the chat you shop from.
          </p>
        </div>
        <div className="mt-4 space-y-4">
          <TopUpButton />
          <SpendLimitEditor currentPaise={account?.spend_limit_paise ?? 0} />
          <LinkTelegram />
        </div>

        <div className="mt-8">
          <Card
            title="Your runs"
            source="One row per request you made"
            action={
              <Link
                className="text-sm text-moss hover:underline"
                href="/dashboard/runs"
              >
                Open the deal room
              </Link>
            }
          >
            <Rows
              items={runs.map((run) => ({
                key: run.run_id,
                left: (
                  <Link
                    className="hover:underline"
                    href={`/dashboard/runs?run=${run.run_id}`}
                  >
                    {run.request ?? "request not recorded"}
                    {run.product_name ? ` (${run.product_name})` : ""}
                  </Link>
                ),
                right: (
                  <>
                    <span className="font-semibold text-moss">
                      {run.outcome ?? "open"}
                    </span>
                    {run.final_amount_paise ? (
                      <span className="ml-2">
                        {money(run.final_amount_paise)}
                      </span>
                    ) : null}
                  </>
                ),
              }))}
            />
          </Card>
        </div>
        <div className="mt-8 grid gap-8 lg:grid-cols-2">
          <section className="border border-ink/10 bg-white p-5">
            <h2 className="text-lg font-semibold">Recent orders</h2>
            <div className="mt-4 divide-y divide-ink/10">
              {orders.length === 0 ? (
                <p className="py-4 text-sm text-ink/70">No orders yet.</p>
              ) : (
                orders.map((order) => (
                  <div
                    key={order.id}
                    className="flex items-center justify-between gap-4 py-3 text-sm"
                  >
                    <div>
                      <p className="font-medium">{order.id.slice(0, 8)}</p>
                      <p className="text-xs text-ink/70">
                        {formatDate(order.created_at)}
                      </p>
                    </div>
                    <div className="text-right">
                      <p className="font-semibold">
                        {money(order.amount_paise)}
                      </p>
                      <p className="text-xs text-moss">
                        {plainWords(order.status)}
                      </p>
                    </div>
                  </div>
                ))
              )}
            </div>
          </section>
          <section className="border border-ink/10 bg-white p-5">
            <h2 className="text-lg font-semibold">Wallet movements</h2>
            <div className="mt-4 divide-y divide-ink/10">
              {ledger.length === 0 ? (
                <p className="py-4 text-sm text-ink/70">
                  No wallet movements yet.
                </p>
              ) : (
                ledger.map((entry) => (
                  <div
                    key={entry.id}
                    className="flex items-center justify-between gap-4 py-3 text-sm"
                  >
                    <div>
                      <p className="font-medium">
                        {plainWords(entry.entry_type)}
                      </p>
                      <p className="text-xs text-ink/70">
                        {formatDate(entry.created_at)}
                      </p>
                    </div>
                    <div className="text-right">
                      <p className="font-semibold">
                        {entry.amount_paise >= 0 ? "+" : ""}
                        {money(entry.amount_paise)}
                      </p>
                      <p className="text-xs text-ink/70">
                        Balance {money(entry.balance_after_paise)}
                      </p>
                    </div>
                  </div>
                ))
              )}
            </div>
          </section>
          <section className="border border-ink/10 bg-white p-5">
            <h2 className="text-lg font-semibold">Merchant revenue</h2>
            <div className="mt-4 divide-y divide-ink/10">
              {revenue.length === 0 ? (
                <p className="py-4 text-sm text-ink/70">
                  No fulfilled revenue yet.
                </p>
              ) : (
                revenue.slice(0, 5).map((row) => (
                  <div key={row.order_id} className="py-3 text-sm">
                    <div className="flex items-center justify-between gap-4">
                      <div>
                        <p className="font-medium">
                          Order {row.order_id.slice(0, 8)}
                        </p>
                        <p className="text-xs text-ink/70">
                          {formatDate(row.credited_at)}
                        </p>
                      </div>
                      <p
                        className={`font-semibold ${row.uplift_paise < 0 ? "text-coral" : "text-moss"}`}
                      >
                        {row.uplift_paise < 0 ? "" : "+"}
                        {money(row.uplift_paise)}
                      </p>
                    </div>
                    <p className="mt-1 text-xs text-ink/70">
                      Base {money(row.base_amount_paise)} to final{" "}
                      {money(row.final_amount_paise)}
                    </p>
                  </div>
                ))
              )}
            </div>
          </section>
          <AuditTimeline events={auditEvents} />
        </div>
      </div>
    </main>
  );
}
