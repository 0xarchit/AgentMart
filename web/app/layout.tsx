// Root layout for the dashboard application.
import type { Metadata } from "next";
import "./globals.css";
import Script from "next/script";

export const metadata: Metadata = {
  title: "AgentMart dashboard",
  description: "Wallet commerce and merchant revenue operations.",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body><Script src="https://checkout.razorpay.com/v1/checkout.js" strategy="afterInteractive" />{children}</body>
    </html>
  );
}
