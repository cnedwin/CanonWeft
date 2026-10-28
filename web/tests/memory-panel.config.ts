import { defineConfig } from "@playwright/test";
import path from "node:path";
export default defineConfig({
  testDir: ".",
  testMatch: [
    "memory-panel.spec.ts",
    "memory-session-recovery.spec.ts",
    "memory-summary.spec.ts",
  ],
  outputDir: "../test-results/memory-panel",
  workers: 1,
  retries: 0,
  use: { baseURL: "http://127.0.0.1:18867", screenshot: "only-on-failure" },
  webServer: {
    command: `"${process.execPath}" node_modules/vite/bin/vite.js --host 127.0.0.1 --port 18867 --strictPort`,
    cwd: path.resolve(import.meta.dirname, ".."),
    url: "http://127.0.0.1:18867/tests/fixtures/memory-panel.html",
    reuseExistingServer: false,
  },
});
