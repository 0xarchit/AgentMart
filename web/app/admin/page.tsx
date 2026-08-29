// Cross-account operations view: revenue, margin, stock and the latest runs.
// Every figure here is derived from rows read below, never from a constant.
import { createAdminClient } from "@/lib/supabase/admin";
import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";
import Link from "next/link";
import { Bars, Card, Empty, Rows, Stat, TopNav, money } from "../ui";
import {
  summarize,
  type OrderRow,
  type ProductRow,
  type RevenueRow,
} from "@/lib/metrics";

type RunRow = {
  run_id: string;
  request: string | null;
  product_name: string | null;
  outcome: string | null;
  final_amount_paise: number | null;
  started_at: string;
};

export default async function AdminPage() {
  const supabase = await createClient();
  const {
    data: { user },
  } = await supabase.auth.getUser();
  if (!user) redirect("/login");
  const admins = (process.env.ADMIN_EMAILS ?? "")
    .split(",")
    .map((email) => email.trim().toLowerCase())
    .filter(Boolean);
  if (admins.length > 0 && !admins.includes((user.email ?? "").toLowerCase()))
    redirect("/dashboard");
  // RLS scopes every table to the caller's own account, so the cross-account
  // admin views must read through the service-role client after the guard.
  const admin = createAdminClient();
  const [revenueResult, ordersResult, productsResult, runsResult, auditResult] =
    await Promise.all([
      admin
        .from("merchant_revenue")
        .select(
          "order_id,base_amount_paise,final_amount_paise,uplift_paise,credited_at",
        )
        .order("credited_at", { ascending: false })
        .limit(200),
      admin
        .from("orders")
        .select("id,product_id,qty,amount_paise,status,created_at")
        .order("created_at", { ascending: false })
        .limit(200),
      admin.from("products").select("id,name,stock,cost_paise,price_paise"),
      admin
        .from("run_summary")
        .select(
          "run_id,request,product_name,outcome,final_amount_paise,started_at",
        )
        .order("started_at", { ascending: false })
        .limit(6),
      admin.from("audit_log").select("id").limit(500),
    ]);

  const revenue = (revenueResult.data ?? []) as RevenueRow[];
  const orders = (ordersResult.data ?? []) as OrderRow[];
  const products = (productsResult.data ?? []) as ProductRow[];
  const runs = (runsResult.data ?? []) as RunRow[];
  const auditEvents = auditResult.data?.length ?? 0;
  const figures = summarize(orders, products, revenue);

  return (
    <main className="min-h-screen bg-paper px-6 py-10">
      <div className="mx-auto max-w-6xl">
        <TopNav current="admin" />
        <div className="mt-8">
          <p className="text-xs font-semibold uppercase tracking-[0.18em] text-moss">
            Platform operations
          </p>
          <h1 className="mt-2 text-4xl font-semibold">Merchant operations</h1>
          <p className="mt-2 text-sm text-ink/60">
            Revenue the negotiating agents produced, the margin they held above
            cost, and what stock they are working with.
          </p>
        </div>

        <div className="mt-8 grid gap-4 md:grid-cols-4">
          <Stat
            label="Revenue above list"
            value={money(figures.uplift)}
            basis={`${revenue.length} credited row(s)`}
            tone="moss"
          />
          <Stat
            label="Settled value"
            value={money(figures.settledValue)}
            basis={`${figures.settledCount} settled order(s)`}
          />
          <Stat
            label="Margin held above cost"
            value={`${money(figures.margin)} (${figures.marginPct}%)`}
            basis={`${figures.pricedCount} order(s) with a cost floor`}
          />
          <Stat
            label="Refunded"
            value={String(figures.refundedCount)}
            basis={`${auditEvents} recorded trail event(s)`}
            tone={figures.refundedCount > 0 ? "coral" : undefined}
          />
        </div>

        <div className="mt-8 grid gap-6 lg:grid-cols-2">
          <Card title="Sales over time" source="Settled orders by day">
            <Bars data={figures.salesOverTime} format={money} />
          </Card>

          <Card title="Top products" source="Settled orders grouped by product">
            <Rows
              items={figures.topProducts.map((entry) => ({
                key: entry.name,
                left: entry.name,
                right: (
                  <>
                    <span className="font-semibold text-moss">
                      {money(entry.value)}
                    </span>
                    <span className="ml-2 text-ink/50">
                      {entry.count} unit(s)
                    </span>
                  </>
                ),
              }))}
            />
          </Card>

          <Card title="Stock health" source="Lowest stock in the catalog">
            <Rows
              items={figures.lowStock.map((product) => ({
                key: product.id,
                left: product.name,
                right: (
                  <span
                    className={
                      product.stock <= 3
                        ? "font-semibold text-coral"
                        : "text-ink/70"
                    }
                  >
                    {product.stock} in stock
                  </span>
                ),
              }))}
            />
          </Card>

          <Card
            title="Latest runs"
            source="One row per request a person made"
            action={
              <Link
                className="text-sm text-moss hover:underline"
                href="/dashboard/runs"
              >
                Open the deal room
              </Link>
            }
          >
            {runs.length === 0 ? (
              <Empty>No runs recorded yet.</Empty>
            ) : (
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
            )}
          </Card>
        </div>

        <Card title="Revenue ledger" source="Credited rows, newest first">
          <Rows
            items={revenue.slice(0, 12).map((row) => ({
              key: row.order_id,
              left: `Order ${row.order_id.slice(0, 8)}`,
              right: (
                <>
                  <span className="text-ink/60">
                    {money(row.final_amount_paise)} settled
                  </span>
                  <span className="ml-3 font-semibold text-moss">
                    +{money(row.uplift_paise)}
                  </span>
                </>
              ),
            }))}
          />
        </Card>
      </div>
    </main>
  );
}
