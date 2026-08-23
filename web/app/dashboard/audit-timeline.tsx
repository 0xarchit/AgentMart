// Client-side filters for account-scoped audit events.
"use client";

import { useMemo, useState } from "react";

export interface AuditEvent {
  id: number;
  order_id: string | null;
  actor: string;
  action: string;
  reason: string | null;
  created_at: string;
}

interface AuditTimelineProps {
  events: AuditEvent[];
}

function formatTimestamp(value: string): string {
  return new Intl.DateTimeFormat("en-IN", { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function actionTone(action: string): string {
  if (action.includes("reject") || action.includes("failed")) return "bg-coral/10 text-coral";
  if (action.includes("approve") || action.includes("fulfilled") || action.includes("topup")) return "bg-mint text-moss";
  return "bg-ink/5 text-ink/70";
}

export function AuditTimeline({ events }: AuditTimelineProps) {
  const [action, setAction] = useState("all");
  const [orderQuery, setOrderQuery] = useState("");
  const actions = useMemo(() => Array.from(new Set(events.map((event) => event.action))).sort(), [events]);
  const visibleEvents = useMemo(() => {
    const query = orderQuery.trim().toLowerCase();
    return events.filter((event) => {
      const matchesAction = action === "all" || event.action === action;
      const matchesOrder = query === "" || event.order_id?.toLowerCase().includes(query);
      return matchesAction && matchesOrder;
    });
  }, [action, events, orderQuery]);

  return (
    <section className="border border-ink/10 bg-white p-5 lg:col-span-2">
      <div className="flex flex-col gap-4 border-b border-ink/10 pb-4 md:flex-row md:items-end md:justify-between">
        <div>
          <h2 className="text-lg font-semibold">Audit timeline</h2>
          <p className="mt-1 text-sm text-ink/60">Account-scoped decisions, wallet movements, and fulfillment outcomes.</p>
        </div>
        <div className="flex flex-col gap-3 sm:flex-row">
          <label className="flex flex-col gap-1 text-xs font-semibold uppercase tracking-[0.12em] text-ink/60">
            Action
            <select value={action} onChange={(event) => setAction(event.target.value)} className="min-w-44 border border-ink/20 bg-white px-3 py-2 text-sm font-normal normal-case tracking-normal text-ink">
              <option value="all">All actions</option>
              {actions.map((value) => <option key={value} value={value}>{value}</option>)}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-xs font-semibold uppercase tracking-[0.12em] text-ink/60">
            Order
            <input value={orderQuery} onChange={(event) => setOrderQuery(event.target.value)} placeholder="Order ID" className="border border-ink/20 px-3 py-2 text-sm font-normal normal-case tracking-normal text-ink" />
          </label>
        </div>
      </div>
      <div className="divide-y divide-ink/10">
        {visibleEvents.length === 0 ? <p className="py-6 text-sm text-ink/50">No audit events match these filters.</p> : visibleEvents.map((event) => (
          <article key={event.id} className="grid gap-2 py-4 text-sm md:grid-cols-[9rem_1fr_auto] md:items-start">
            <p className="text-xs text-ink/50">{formatTimestamp(event.created_at)}</p>
            <div>
              <div className="flex flex-wrap items-center gap-2">
                <span className={`px-2 py-1 text-xs font-semibold ${actionTone(event.action)}`}>{event.action}</span>
                <span className="text-xs text-ink/50">by {event.actor}</span>
              </div>
              <p className="mt-2 text-ink/70">{event.reason || "No reason recorded."}</p>
            </div>
            <p className="font-mono text-xs text-ink/50">{event.order_id ? event.order_id.slice(0, 8) : "No order"}</p>
          </article>
        ))}
      </div>
    </section>
  );
}
