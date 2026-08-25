// Client control for updating the account spend limit from the dashboard.
"use client";

import { useState } from "react";

export function SpendLimitEditor({ currentPaise }: { currentPaise: number }) {
  const [rupees, setRupees] = useState(String(Math.round(currentPaise / 100)));
  const [message, setMessage] = useState("");
  const [saved, setSaved] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    setMessage("");
    const amountPaise = Math.round(Number(rupees) * 100);
    const response = await fetch("/api/account/spend-limit", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ spend_limit_paise: amountPaise }),
    });
    const payload = await response.json();
    if (!response.ok) {
      setMessage(payload.error ?? "Unable to update spend limit");
    } else {
      setSaved(payload.spend_limit_paise);
      setMessage("Spend limit updated.");
    }
    setBusy(false);
  }

  return (
    <section className="border border-ink/10 bg-white p-5">
      <h2 className="text-lg font-semibold">Agent spending limit</h2>
      <p className="mt-1 text-sm text-ink/60">
        Purchases above this amount require your approval in Telegram. Applies to
        agent buys only — wallet top-ups are unaffected.
      </p>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-sm font-medium">
          ₹
          <input
            value={rupees}
            onChange={(event) => setRupees(event.target.value)}
            inputMode="decimal"
            className="w-28 border border-ink/20 px-3 py-2"
            aria-label="Spend limit in rupees"
          />
        </label>
        <button
          type="button"
          onClick={save}
          disabled={busy}
          className="bg-ink px-4 py-2 text-sm font-semibold text-paper disabled:opacity-50"
        >
          {busy ? "Saving..." : "Update limit"}
        </button>
      </div>
      {saved !== null ? (
        <p className="mt-3 text-sm text-moss">Active limit: ₹{(saved / 100).toLocaleString("en-IN")}</p>
      ) : null}
      {message ? <p className="mt-3 text-sm text-coral">{message}</p> : null}
    </section>
  );
}
