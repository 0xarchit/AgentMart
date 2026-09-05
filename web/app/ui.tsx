// Small presentation pieces shared by the storefront, dashboard and admin pages.
// Server-component safe: no hooks, no client state, no chart dependency.
import Link from "next/link";
import type { ReactNode } from "react";
import { plainWords } from "@/lib/words";

/** Card is a titled panel. The source line names the rows a figure came from. */
export function Card({
  title,
  source,
  action,
  children,
}: {
  title: string;
  source?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="border border-ink/10 bg-white p-6">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <h2 className="text-xl font-semibold">{title}</h2>
        {action}
      </div>
      {source ? <p className="mt-1 text-xs text-ink/70">{source}</p> : null}
      <div className="mt-4">{children}</div>
    </section>
  );
}

/** Stat shows one figure with the row count or basis it was derived from. */
export function Stat({
  label,
  value,
  basis,
  tone,
}: {
  label: string;
  value: string;
  basis?: string;
  tone?: "moss" | "coral";
}) {
  const colour =
    tone === "moss" ? "text-moss" : tone === "coral" ? "text-coral" : "";
  return (
    <div className="border border-ink/10 bg-white p-5">
      <p className="text-sm text-ink/60">{label}</p>
      <p className={`mt-3 text-3xl font-semibold ${colour}`}>{value}</p>
      {basis ? <p className="mt-2 text-xs text-ink/70">{basis}</p> : null}
    </div>
  );
}

/**
 * Outcome is one run's result, worded and coloured the same way everywhere it is
 * listed. Mint is money that moved as asked, coral is a run that did not get what
 * it went for, and plain ink is everything that settled without a purchase.
 *
 * The values compared here are run_summary's own, which are lower case. Three
 * pages rendered this chip themselves: one matched upper case names that never
 * arrive and so painted every completed purchase in the refusal colour, and the
 * other two printed the raw column in green whatever it said.
 */
export function Outcome({ outcome }: { outcome: string | null }) {
  const tone =
    outcome === "buy"
      ? "bg-mint text-moss"
      : outcome === null || outcome === "ask_human" || outcome === "refunded"
        ? "bg-ink/5 text-ink/70"
        : "bg-coral/10 text-coral";
  return (
    <span className={`px-2 py-1 text-xs font-semibold ${tone}`}>
      {outcome === null ? "no outcome recorded" : plainWords(outcome)}
    </span>
  );
}

/** Bars draws a value per label as plain height-scaled blocks. */
export function Bars({
  data,
  format,
}: {
  data: { label: string; value: number }[];
  format: (value: number) => string;
}) {
  if (data.length === 0) {
    return <Empty>Nothing recorded yet.</Empty>;
  }
  const peak = Math.max(...data.map((point) => point.value), 1);
  return (
    <div className="flex items-end gap-3 overflow-x-auto pb-1">
      {data.map((point) => (
        <div
          key={point.label}
          className="flex w-16 shrink-0 flex-col items-center gap-2"
        >
          <span className="text-xs font-medium text-ink/70">
            {format(point.value)}
          </span>
          <div
            className="w-full bg-moss"
            style={{ height: `${Math.max(4, (point.value / peak) * 120)}px` }}
            title={`${point.label}: ${format(point.value)}`}
          />
          <span className="text-xs text-ink/70">{point.label}</span>
        </div>
      ))}
    </div>
  );
}

/** Empty is the placeholder for a panel whose table has no rows yet. */
export function Empty({ children }: { children: ReactNode }) {
  return (
    <p className="border border-dashed border-ink/20 px-4 py-6 text-sm text-ink/70">
      {children}
    </p>
  );
}

/** Rows lays out label and value pairs as a divided list. */
export function Rows({
  items,
}: {
  items: { key: string; left: ReactNode; right: ReactNode }[];
}) {
  if (items.length === 0) {
    return <Empty>Nothing recorded yet.</Empty>;
  }
  return (
    <div className="divide-y divide-ink/10">
      {items.map((item) => (
        <div
          key={item.key}
          className="flex items-center justify-between gap-4 py-3 text-sm"
        >
          <span className="min-w-0">{item.left}</span>
          <span className="shrink-0 text-right">{item.right}</span>
        </div>
      ))}
    </div>
  );
}

/** Skeleton is the placeholder a page shows while its data is on the way, so a
 * slow read looks like loading rather than like an empty account. */
export function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div className="animate-pulse space-y-3" aria-hidden="true">
      {Array.from({ length: lines }, (_, index) => (
        <div key={index} className="h-16 bg-mint/60" />
      ))}
    </div>
  );
}

/** Loading is one whole page of placeholder, navigation included, so the frame
 * does not jump when the real content arrives. */
export function Loading({
  current,
  title,
}: {
  current: "store" | "dashboard" | "runs" | "admin";
  title: string;
}) {
  return (
    <main className="min-h-screen bg-paper px-6 py-10">
      <div className="mx-auto max-w-6xl">
        <TopNav current={current} />
        <div className="mt-8 border-b border-ink/10 pb-6">
          <h1 className="text-3xl font-semibold">{title}</h1>
          <p className="mt-2 text-sm text-ink/70">
            Reading the latest figures.
          </p>
        </div>
        <div className="mt-8">
          <Skeleton lines={4} />
        </div>
      </div>
    </main>
  );
}

/** TopNav is the one navigation bar every page shares. */
export function TopNav({
  current,
  action,
}: {
  current: "store" | "dashboard" | "runs" | "admin";
  action?: ReactNode;
}) {
  const link = (href: string, label: string, key: string) => (
    <Link
      key={key}
      href={href}
      className={
        current === key
          ? "font-semibold text-moss"
          : "text-ink/70 hover:text-moss"
      }
    >
      {label}
    </Link>
  );
  return (
    <nav className="flex flex-wrap items-center justify-between gap-4 border-b border-ink/10 pb-5">
      <Link className="font-semibold text-moss" href="/">
        AgentMart
      </Link>
      <div className="flex flex-wrap items-center gap-4 text-sm">
        {link("/", "Shop", "store")}
        {link("/dashboard", "Your account", "dashboard")}
        {link("/dashboard/runs", "Deal room", "runs")}
        {link("/admin", "Merchant", "admin")}
        {action}
      </div>
    </nav>
  );
}
