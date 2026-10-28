import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  workers: 1,
  retries: 0,
  use: {
    baseURL: "http://127.0.0.1:18865",
    screenshot: "only-on-failure",
    actionTimeout: 10000,
  },
});
