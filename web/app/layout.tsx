// Root layout: the one place the typeface, the metadata and the checkout script
// are set for every page.
import type { Metadata } from "next";
import { DM_Sans } from "next/font/google";
import "./globals.css";
import Script from "next/script";

// One typeface for the whole site, self hosted at build time so no request goes
// to a third party when a page loads.
const sans = DM_Sans({
  subsets: ["latin"],
  display: "swap",
  variable: "--font-sans",
});

export const metadata: Metadata = {
  title: "AgentMart",
  description: "A shop an agent can buy from, with every rupee accounted for.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" className={sans.variable}>
      <body className="font-sans antialiased">
        <Script
          src="https://checkout.razorpay.com/v1/checkout.js"
          strategy="afterInteractive"
        />
        {children}
      </body>
    </html>
  );
}
