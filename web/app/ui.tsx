// Small presentation pieces shared by the storefront, dashboard and admin pages.
// Server-component safe: no hooks, no client state, no chart dependency.
import Link from "next/link";
import type { ReactNode } from "react";

/** money renders paise as rupees, the only currency this project handles. */
export function money(paise: number): string {
  return `₹${(paise / 100).toLocaleString("en-IN", { maximumFractionDigits: 0 })}`;
}

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
      {source ? <p className="mt-1 text-xs text-ink/50">{source}</p> : null}
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
      {basis ? <p className="mt-2 text-xs text-ink/50">{basis}</p> : null}
    </div>
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
          <span className="text-xs text-ink/50">{point.label}</span>
        </div>
      ))}
    </div>
  );
}

/** Empty is the placeholder for a panel whose table has no rows yet. */
export function Empty({ children }: { children: ReactNode }) {
  return (
    <p className="border border-dashed border-ink/20 px-4 py-6 text-sm text-ink/50">
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

/** TopNav is the one navigation bar every page shares. */
export function TopNav({
  current,
}: {
  current: "store" | "dashboard" | "admin";
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
    <nav className="flex items-center justify-between border-b border-ink/10 pb-5">
      <Link className="font-semibold text-moss" href="/">
        AgentMart
      </Link>
      <div className="flex gap-4 text-sm">
        {link("/", "Storefront", "store")}
        {link("/dashboard", "User portal", "dashboard")}
        {link("/admin", "Admin", "admin")}
      </div>
    </nav>
  );
}
