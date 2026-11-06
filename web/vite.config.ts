import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  server: {
    host: "127.0.0.1",
    port: Number(process.env.SUMI_VITE_PORT ?? 5173),
    proxy: {
      "/api": {
        target: process.env.SUMI_VITE_PROXY_TARGET ?? "http://127.0.0.1:3000",
        ws: true,
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    exclude: ["e2e/**", "node_modules/**"],
    // Component tests render whole routes in jsdom; on a loaded machine one test can take several
    // seconds. These limits only bound how long a failing test waits, not how long a passing one runs.
    testTimeout: 30_000,
  },
});
