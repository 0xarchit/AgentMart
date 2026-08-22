// Tailwind theme configuration for the dashboard.
import type { Config } from "tailwindcss";

const config: Config = {
  content: ["./app/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        ink: "#15201b",
        moss: "#335c45",
        mint: "#dce9df",
        paper: "#f7f7f2",
        coral: "#c9654a",
      },
    },
  },
  plugins: [],
};

export default config;
