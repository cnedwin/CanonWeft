import { expect, type BrowserContext, type Page } from "@playwright/test";

// One real owner login per browser worker. Feature tests reuse only the live
// session cookie in memory; the dedicated workspace test still exercises login
// and logout through the UI. Production login throttling remains unchanged.
let cookies: Awaited<ReturnType<BrowserContext["cookies"]>> = [];

export async function loginAsOwner(page: Page) {
  if (cookies.length) {
    await page.context().addCookies(cookies);
    const current = await page.request.get("/api/me");
    if (current.ok() && (await current.json()).username === "owner") {
      await page.goto("/");
      await expect(page.getByLabel("项目名称", { exact: true })).toBeVisible();
      return;
    }
    cookies = [];
    await page.context().clearCookies({ name: "studio_session" });
  }
  await page.goto("/");
  await page.getByLabel("用户名", { exact: true }).fill("owner");
  await page
    .getByLabel("密码", { exact: true })
    .fill("test-owner-password-123");
  const response = page.waitForResponse(
    (r) => r.url().endsWith("/api/login") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "登录", exact: true }).click();
  expect((await response).status()).toBe(200);
  await expect(page.getByLabel("项目名称", { exact: true })).toBeVisible();
  cookies = (await page.context().cookies()).filter(
    (cookie) => cookie.name === "studio_session",
  );
  expect(cookies).toHaveLength(1);
}
