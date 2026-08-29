// Cross-account operations view: revenue, margin, stock and the latest runs.
// Every figure here is derived from rows read below, never from a constant.
import { createAdminClient } from "@/lib/supabase/admin";
import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";
import Link from "next/link";
import { Bars, Card, Empty, Rows, Stat, TopNav, money } from "../ui";

type OrderRow = {
  id: string;
  product_id: string;
  qty: number;
  amount_paise: number;
  status: string;
  created_at: string;
};

type ProductRow = {
  id: string;
  name: string;
  stock: number;
  cost_paise: number;
  price_paise: number;
};

type RunRow = {
  run_id: string;
  request: string | null;
  product_name: string | null;
  outcome: string | null;
  final_amount_paise: number | null;
  started_at: string;
};

const settled = (status: string) => status.startsWith("fulfilled");
const day = (iso: string) =>
  new Date(iso).toLocaleDateString("en-IN", { day: "2-digit", month: "short" });

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
        .select("order_id,base_amount_paise,final_amount_paise,uplift_paise,credited_at")
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
        .select("run_id,request,product_name,outcome,final_amount_paise,started_at")
        .order("started_at", { ascending: false })
        .limit(6),
      admin.from("audit_log").select("id").limit(500),
    ]);

  const revenue = revenueResult.data ?? [];
  const orders = (ordersResult.data ?? []) as OrderRow[];
  const products = (productsResult.data ?? []) as ProductRow[];
  const runs = (runsResult.data ?? []) as RunRow[];
  const auditEvents = auditResult.data?.length ?? 0;

  const productById = new Map(products.map((product) => [product.id, product]));
  const fulfilled = orders.filter((order) => settled(order.status));
  const refunded = orders.filter((order) => order.status.startsWith("refunded"));

  const uplift = revenue.reduce((sum, row) => sum + (row.uplift_paise ?? 0), 0);
  const settledValue = fulfilled.reduce((sum, order) => sum + order.amount_paise, 0);

  // Margin is what the settled price held above the merchant's own cost floor.
  // Orders whose product row is missing a cost are left out rather than guessed.
  const priced = fulfilled.filter(
    (order) => (productById.get(order.product_id)?.cost_paise ?? 0) > 0,
  );
  const margin = priced.reduce((sum, order) => {
    const cost = (productById.get(order.product_id)?.cost_paise ?? 0) * order.qty;
    return sum + (order.amount_paise - cost);
  }, 0);
  const pricedValue = priced.reduce((sum, order) => sum + order.amount_paise, 0);
  const marginPct = pricedValue > 0 ? Math.round((margin / pricedValue) * 100) : 0;

  // Sales over time: one bar per day that has at least one settled order.
  const perDay = new Map<string, number>();
  for (const order of [...fulfilled].reverse()) {
    const label = day(order.created_at);
    perDay.set(label, (perDay.get(label) ?? 0) + order.amount_paise);
  }
  const salesOverTime = [...perDay.entries()]
    .slice(-10)
    .map(([label, value]) => ({ label, value }));

  // Top products by settled value, so the leader is revenue and not row count.
  const perProduct = new Map<string, { name: string; count: number; value: number }>();
  for (const order of fulfilled) {
    const name = productById.get(order.product_id)?.name ?? "Unknown product";
    const entry = perProduct.get(order.product_id) ?? { name, count: 0, value: 0 };
    entry.count += order.qty;
    entry.value += order.amount_paise;
    perProduct.set(order.product_id, entry);
  }
  const topProducts = [...perProduct.values()]
    .sort((a, b) => b.value - a.value)
    .slice(0, 6);

  const lowStock = [...products].sort((a, b) => a.stock - b.stock).slice(0, 6);

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
            value={money(uplift)}
            basis={`${revenue.length} credited row(s)`}
            tone="moss"
          />
          <Stat
            label="Settled value"
            value={money(settledValue)}
            basis={`${fulfilled.length} settled order(s)`}
          />
          <Stat
            label="Margin held above cost"
            value={`${money(margin)} (${marginPct}%)`}
            basis={`${priced.length} order(s) with a cost floor`}
          />
          <Stat
            label="Refunded"
            value={String(refunded.length)}
            basis={`${auditEvents} recorded trail event(s)`}
            tone={refunded.length > 0 ? "coral" : undefined}
          />
        </div>

        <div className="mt-8 grid gap-6 lg:grid-cols-2">
          <Card title="Sales over time" source="Settled orders by day">
            <Bars data={salesOverTime} format={money} />
          </Card>

          <Card title="Top products" source="Settled orders grouped by product">
            <Rows
              items={topProducts.map((entry) => ({
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
              items={lowStock.map((product) => ({
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
              <Link className="text-sm text-moss hover:underline" href="/dashboard/runs">
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

        <Card
          title="Revenue ledger"
          source="Credited rows, newest first"
        >
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
