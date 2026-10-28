import { defineConfig } from "@playwright/test";
import path from "node:path";
export default defineConfig({
  testDir: ".",
  testMatch: "author-workbench.spec.ts",
  outputDir: "../test-results/author-workbench",
  workers: 1,
  retries: 0,
  use: {
    channel: "chrome",
    baseURL: "http://127.0.0.1:18874",
    screenshot: "only-on-failure",
  },
  webServer: process.env.STUDIO_AUTHOR_EXTERNAL
    ? undefined
    : {
        command: `"${process.execPath}" node_modules/vite/bin/vite.js --configLoader native --host 127.0.0.1 --port 18874 --strictPort`,
        cwd: path.resolve(import.meta.dirname, ".."),
        url: "http://127.0.0.1:18874",
        reuseExistingServer: false,
      },
});
