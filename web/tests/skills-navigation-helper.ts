import type { Page } from "@playwright/test";

export async function openSkillsSection(
  page: Page,
  name: "能力与工作流" | "方法包与版本" | "来源发现" | "独立评测与绑定",
) {
  await page
    .getByRole("navigation", { name: "方法与能力分区", exact: true })
    .getByRole("button", { name, exact: true })
    .click();
}
