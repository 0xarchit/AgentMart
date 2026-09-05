// Protected dashboard landing page.
import { signOut } from "@/app/auth/actions";
import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";
import Link from "next/link";
import { TopUpButton } from "@/app/dashboard/topup-button";
import { LinkTelegram } from "@/app/dashboard/link-telegram";
import { SpendLimitEditor } from "@/app/dashboard/spend-limit-editor";
import { AuditTimeline, type AuditEvent } from "@/app/dashboard/audit-timeline";
import { Card, Outcome, Rows, Stat, TopNav } from "@/app/ui";
import { ledgerMoney, money } from "@/lib/money";
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
  // A refused or broken read is not an empty wallet. Rendering it as one told the
  // person their money was gone, and prefilled the limit editor with zero, which
  // a single tap would then have made true.
  const accountUnreadable = accountResult.error !== null;
  const orders = (ordersResult.data ?? []) as Order[];
  const orderCount = orderCountResult.count ?? orders.length;
  const ledger = (ledgerResult.data ?? []) as LedgerEntry[];
  const auditEvents = (auditResult.data ?? []) as AuditEvent[];
  const runs = (runsResult.data ?? []) as Run[];
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
          <p className="mt-2 text-sm text-ink/70">Signed in as {user.email}</p>
        </div>
        <div className="mt-8 grid gap-4 md:grid-cols-2">
          <Stat
            label="Wallet balance"
            value={
              accountUnreadable
                ? "Unavailable"
                : money(account?.wallet_balance_paise ?? 0)
            }
            basis={
              accountUnreadable
                ? "Your account could not be read just now, so this is not a balance. Reload before acting on this page."
                : `Spend limit ${money(account?.spend_limit_paise ?? 0)}`
            }
            tone={accountUnreadable ? "coral" : undefined}
          />
          <Stat
            label="Orders placed"
            value={String(orderCount)}
            basis="Only ever your own account"
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
          {accountUnreadable ? (
            <section className="border border-coral/40 bg-white p-5 text-sm text-ink/70">
              The spending limit is hidden because your account could not be read.
              Editing it from a guessed figure would set a limit you never chose.
            </section>
          ) : (
            <SpendLimitEditor currentPaise={account?.spend_limit_paise ?? 0} />
          )}
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
                    {run.request ?? "Nothing was asked for in words"}
                    {run.product_name ? ` (${run.product_name})` : ""}
                  </Link>
                ),
                right: (
                  <>
                    <Outcome outcome={run.outcome} />
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
                        {ledgerMoney(entry.entry_type, entry.amount_paise)}
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
          <AuditTimeline events={auditEvents} />
        </div>
      </div>
    </main>
  );
}
