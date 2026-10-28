import { defineConfig } from "@playwright/test";
import path from "node:path";

export default defineConfig({
  testDir: ".",
  testMatch: "novel-entry.spec.ts",
  outputDir: "../test-results/novel-entry",
  workers: 1,
  retries: 0,
  use: { baseURL: "http://127.0.0.1:18869", screenshot: "only-on-failure" },
  webServer: {
    command: `"${process.execPath}" node_modules/vite/bin/vite.js --host 127.0.0.1 --port 18869 --strictPort`,
    cwd: path.resolve(import.meta.dirname, ".."),
    url: "http://127.0.0.1:18869",
    reuseExistingServer: false,
  },
});
