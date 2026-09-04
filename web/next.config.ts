// Next.js configuration for the dashboard application.
import type { NextConfig } from "next";

// Standalone output is what the container images run: they copy .next/standalone and
// start node server.js from it. It is also what a managed host must not be handed,
// because Vercel builds the default output and its final step reads a trace file that
// standalone never writes, so the build fails after compiling successfully. The images
// ask for it by name and every other build gets the default.
const nextConfig: NextConfig = {
  output: process.env.NEXT_OUTPUT_STANDALONE ? "standalone" : undefined,
};

export default nextConfig;
