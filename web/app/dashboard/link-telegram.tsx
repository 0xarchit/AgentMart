// Client control for generating a Telegram account link token.
"use client";

import { useState } from "react";

export function LinkTelegram() {
  const [token, setToken] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  async function generate() {
    setBusy(true);
    setMessage("");
    const response = await fetch("/api/link-tokens", { method: "POST" });
    const payload = await response.json();
    if (!response.ok) setMessage(payload.error ?? "Unable to generate link token");
    else setToken(payload.token);
    setBusy(false);
  }

  return (
    <section className="border border-ink/10 bg-white p-5">
      <h2 className="text-lg font-semibold">Link Telegram</h2>
      <p className="mt-1 text-sm text-ink/60">Generate a one-time token, then send it with `/link` to the bot.</p>
      <button type="button" onClick={generate} disabled={busy} className="mt-4 border border-ink/20 px-4 py-2 text-sm font-semibold disabled:opacity-50">{busy ? "Generating..." : "Generate link token"}</button>
      {token ? <p className="mt-4 break-all bg-mint px-3 py-2 font-mono text-sm text-moss">{token}</p> : null}
      {message ? <p className="mt-3 text-sm text-coral">{message}</p> : null}
    </section>
  );
}
