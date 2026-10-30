import type { Page } from "@playwright/test";

export async function openModelSettingsSection(
  page: Page,
  name: "模型与 Key" | "专业用途" | "开写检查" | "预算与账单" | "模型价格",
) {
  await page
    .getByRole("navigation", { name: "模型设置分区", exact: true })
    .getByRole("button", { name, exact: true })
    .click();
}
