// The deal room: one shopping run read back as the conversation that set the
// price next to the money that moved because of it.
import { money } from "@/lib/money";
import { currentIdentity } from "@/lib/roles";
import { createAdminClient } from "@/lib/supabase/admin";
import { createClient } from "@/lib/supabase/server";
import { redirect } from "next/navigation";
import Link from "next/link";
import { TopNav } from "@/app/ui";

type RunSummary = {
  run_id: string;
  started_at: string;
  last_at: string;
  events: number;
  request: string | null;
  product_name: string | null;
  outcome: string | null;
  outcome_reason: string | null;
  final_amount_paise: number | null;
};

type Turn = {
  actor: string;
  message: string;
  at: string;
};

type TimelineRow = {
  run_id: string;
  at: string;
  actor: string;
  action: string;
  reason: string | null;
  order_id: string | null;
  amount_paise: number | null;
  gateway_order_id: string | null;
  payload: { run?: { transcript?: Turn[] } } | null;
};

function formatTime(value: string): string {
  return new Intl.DateTimeFormat("en-IN", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function outcomeTone(outcome: string | null): string {
  if (outcome === "BUY") return "bg-mint text-moss";
  if (outcome === "ASK_HUMAN") return "bg-ink/5 text-ink/70";
  return "bg-coral/10 text-coral";
}

export default async function RunsPage({
  searchParams,
}: {
  searchParams: Promise<{ run?: string }>;
}) {
  const identity = await currentIdentity();
  if (!identity) redirect("/login");
  // An operator reaches this page from /admin, which links each run to whichever
  // account earned it. Row level security scopes run_summary and run_timeline to
  // the caller, so the session client returned nothing for another account's run
  // and the deal room reported "Nothing spent" about a settled order. Reading
  // through the service role for an operator opens nothing new: /admin already
  // shows them every run, and the role is read from the caller's own session.
  const operator = identity.role === "admin";
  const supabase = operator ? createAdminClient() : await createClient();

  const { run: selected } = await searchParams;
  const [summaryResult, timelineResult] = await Promise.all([
    supabase
      .from("run_summary")
      .select(
        "run_id,started_at,last_at,events,request,product_name,outcome,outcome_reason,final_amount_paise",
      )
      .order("started_at", { ascending: false })
      .limit(25),
    selected
      ? supabase
          .from("run_timeline")
          .select(
            "run_id,at,actor,action,reason,order_id,amount_paise,payload,gateway_order_id",
          )
          .eq("run_id", selected)
          .order("at", { ascending: true })
      : Promise.resolve({ data: [] as TimelineRow[] }),
  ]);

  const runs = (summaryResult.data ?? []) as RunSummary[];
  const timeline = (timelineResult.data ?? []) as TimelineRow[];
  const current = runs.find((run) => run.run_id === selected);
  const conversation: Turn[] =
    timeline.flatMap((row) => row.payload?.run?.transcript ?? []) ?? [];

  return (
    <main className="mx-auto max-w-6xl px-6 py-10">
      <TopNav current="runs" />
      <header className="mt-8 border-b border-ink/10 pb-6">
        <h1 className="text-2xl font-semibold">Deal room</h1>
        <p className="mt-1 text-sm text-ink/70">
          {operator
            ? "Every run the agents completed, across all accounts, with the words and the money side by side."
            : "Every run the agents completed for this account, with the words and the money side by side."}
        </p>
      </header>

      <div className="mt-6 grid gap-6 lg:grid-cols-[20rem_1fr]">
        <section className="border border-ink/10 bg-white">
          <h2 className="border-b border-ink/10 px-4 py-3 text-sm font-semibold uppercase tracking-[0.12em] text-ink/60">
            Runs
          </h2>
          {runs.length === 0 ? (
            <p className="px-4 py-6 text-sm text-ink/70">
              No runs recorded yet. Ask the shopping agent for something in
              chat.
            </p>
          ) : (
            <ul className="divide-y divide-ink/10">
              {runs.map((run) => (
                <li key={run.run_id}>
                  <Link
                    href={`/dashboard/runs?run=${run.run_id}`}
                    className={`block px-4 py-3 text-sm hover:bg-ink/5 ${run.run_id === selected ? "bg-ink/5" : ""}`}
                  >
                    <span className="flex items-center justify-between gap-2">
                      <span
                        className={`px-2 py-1 text-xs font-semibold ${outcomeTone(run.outcome)}`}
                      >
                        {run.outcome ?? "in progress"}
                      </span>
                      <span className="text-xs text-ink/70">
                        {formatTime(run.started_at)}
                      </span>
                    </span>
                    <span className="mt-2 block truncate text-ink/70">
                      {run.request ?? "No request recorded"}
                    </span>
                    {run.final_amount_paise !== null && (
                      <span className="mt-1 block text-xs text-ink/70">
                        {run.product_name} at{" "}
                        {money(run.final_amount_paise)}
                      </span>
                    )}
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>

        {!selected ? (
          <section className="border border-ink/10 bg-white p-6 text-sm text-ink/70">
            Pick a run to read it back.
          </section>
        ) : (
          <div className="grid gap-6">
            <section className="border border-ink/10 bg-white p-5">
              <h2 className="text-lg font-semibold">
                {current?.product_name ?? "Run"}
              </h2>
              <p className="mt-1 text-sm text-ink/60">
                {current?.request ?? "No request recorded"}
              </p>
              <dl className="mt-4 grid gap-4 text-sm sm:grid-cols-4">
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-ink/70">
                    Outcome
                  </dt>
                  <dd className="mt-1">{current?.outcome ?? "in progress"}</dd>
                </div>
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-ink/70">
                    Settled at
                  </dt>
                  <dd className="mt-1">
                    {current?.final_amount_paise != null
                      ? money(current.final_amount_paise)
                      : "Nothing spent"}
                  </dd>
                </div>
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-ink/70">
                    Events
                  </dt>
                  <dd className="mt-1">{current?.events ?? timeline.length}</dd>
                </div>
                <div>
                  <dt className="text-xs uppercase tracking-[0.12em] text-ink/70">
                    Run
                  </dt>
                  <dd className="mt-1 font-mono text-xs">
                    {selected.slice(0, 8)}
                  </dd>
                </div>
              </dl>
              {current?.outcome_reason && (
                <p className="mt-4 border-t border-ink/10 pt-4 text-sm text-ink/70">
                  {current.outcome_reason}
                </p>
              )}
            </section>

            <div className="grid gap-6 lg:grid-cols-2">
              <section className="border border-ink/10 bg-white p-5">
                <h3 className="text-sm font-semibold uppercase tracking-[0.12em] text-ink/60">
                  What was said
                </h3>
                {conversation.length === 0 ? (
                  <p className="mt-4 text-sm text-ink/70">
                    No conversation recorded for this run.
                  </p>
                ) : (
                  <ol className="mt-4 space-y-3">
                    {conversation.map((turn, index) => (
                      <li
                        key={`${turn.at}-${index}`}
                        className={`border-l-2 pl-3 text-sm ${turn.actor === "merchant" ? "border-moss" : "border-ink/30"}`}
                      >
                        <p className="text-xs uppercase tracking-[0.12em] text-ink/70">
                          {turn.actor}
                        </p>
                        <p className="mt-1 text-ink/80">{turn.message}</p>
                      </li>
                    ))}
                  </ol>
                )}
              </section>

              <section className="border border-ink/10 bg-white p-5">
                <h3 className="text-sm font-semibold uppercase tracking-[0.12em] text-ink/60">
                  What it cost
                </h3>
                {timeline.length === 0 ? (
                  <p className="mt-4 text-sm text-ink/70">
                    No trail rows for this run.
                  </p>
                ) : (
                  <ol className="mt-4 divide-y divide-ink/10">
                    {timeline.map((row, index) => (
                      <li key={`${row.at}-${index}`} className="py-3 text-sm">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <span className="text-xs font-semibold text-ink/70">
                            {row.action}
                          </span>
                          <span className="text-xs text-ink/70">
                            {formatTime(row.at)}
                          </span>
                        </div>
                        <p className="mt-1 text-xs text-ink/70">
                          by {row.actor}
                          {row.amount_paise != null &&
                            ` at ${money(row.amount_paise)}`}
                        </p>
                        {row.reason && (
                          <p className="mt-1 text-ink/70">{row.reason}</p>
                        )}
                        {row.gateway_order_id && (
                          <p className="mt-2 text-xs text-ink/70">
                            Gateway order{" "}
                            <code className="select-all rounded bg-mint/60 px-1 py-0.5 font-mono text-ink">
                              {row.gateway_order_id}
                            </code>
                          </p>
                        )}
                      </li>
                    ))}
                  </ol>
                )}
              </section>
            </div>
          </div>
        )}
      </div>
    </main>
  );
}
