import { defineConfig } from "@playwright/test";
import path from "node:path";

export default defineConfig({
  testDir: ".",
  testMatch: "quick-opening.spec.ts",
  outputDir: "../test-results/quick-opening",
  workers: 1,
  retries: 0,
  use: { baseURL: "http://127.0.0.1:18879", screenshot: "only-on-failure" },
  webServer: {
    command: `"${process.execPath}" node_modules/vite/bin/vite.js --host 127.0.0.1 --port 18879 --strictPort`,
    cwd: path.resolve(import.meta.dirname, ".."),
    url: "http://127.0.0.1:18879",
    reuseExistingServer: false,
  },
});
