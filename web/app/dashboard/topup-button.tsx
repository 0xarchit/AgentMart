// Client-side Razorpay Checkout launcher for human wallet funding.
"use client";

import { useState } from "react";

declare global {
  interface Window {
    Razorpay?: new (options: Record<string, unknown>) => { open: () => void };
  }
}

export function TopUpButton() {
  const [amount, setAmount] = useState("1000");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  async function beginTopUp() {
    setBusy(true);
    setMessage("");
    const amountPaise = Math.round(Number(amount) * 100);
    const response = await fetch("/api/topups/orders", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ amount_paise: amountPaise }) });
    const payload = await response.json();
    if (!response.ok) {
      setMessage(payload.error ?? "Unable to start top-up");
      setBusy(false);
      return;
    }
    if (!window.Razorpay) {
      setMessage("Checkout is unavailable until the Razorpay script loads");
      setBusy(false);
      return;
    }
    const checkout = new window.Razorpay({
      key: payload.key_id,
      amount: payload.amount_paise,
      currency: payload.currency,
      name: "AgentMart",
      description: "Wallet top-up",
      order_id: payload.order_id,
      handler: () => setMessage("Payment submitted. Wallet credit appears after webhook verification."),
      modal: { ondismiss: () => setMessage("Checkout closed") },
    });
    checkout.open();
    setBusy(false);
  }

  return (
    <div className="border border-ink/10 bg-white p-5">
      <h2 className="text-lg font-semibold">Fund wallet</h2>
      <p className="mt-1 text-sm text-ink/60">Checkout requires a human. Agent purchases use the internal wallet.</p>
      <div className="mt-4 flex flex-wrap gap-3">
        <label className="flex items-center gap-2 text-sm font-medium">₹<input value={amount} onChange={(event) => setAmount(event.target.value)} inputMode="decimal" className="w-28 border border-ink/20 px-3 py-2" aria-label="Top-up amount in rupees" /></label>
        <button type="button" onClick={beginTopUp} disabled={busy} className="bg-ink px-4 py-2 text-sm font-semibold text-paper disabled:opacity-50">{busy ? "Starting..." : "Open Checkout"}</button>
      </div>
      {message ? <p className="mt-3 text-sm text-moss">{message}</p> : null}
    </div>
  );
}
