// Periodically refreshes server-rendered dashboard data.
"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

export function AuditRefresh() {
  const router = useRouter();

  useEffect(() => {
    const interval = window.setInterval(() => router.refresh(), 15000);
    return () => window.clearInterval(interval);
  }, [router]);

  return <button type="button" onClick={() => router.refresh()} className="border border-ink/20 px-3 py-2 text-sm font-semibold">Refresh events</button>;
}
