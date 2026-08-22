// Root layout for the dashboard application.
import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "AgentMart dashboard",
  description: "Wallet commerce and merchant revenue operations.",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
