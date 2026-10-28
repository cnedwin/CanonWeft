import { expect, type Page } from "@playwright/test";

// Drive the same explicit per-change explanation form used by people.
export async function saveReasonedCandidate(page: Page) {
  const inspect = page.getByRole("button", {
    name: "检查变更并保存候选",
    exact: true,
  });
  if (await inspect.count()) await inspect.click();
  const save = page.getByRole("button", {
    name: "保存为候选版本",
    exact: true,
  });
  // The preview is loaded asynchronously. Do not count zero reason fields
  // while that request is still in flight and accidentally submit blank ones.
  await expect(save).toBeVisible();
  const reasons = page.getByRole("textbox", { name: /^变更理由 / });
  for (let i = 0; i < (await reasons.count()); i++) {
    if (!(await reasons.nth(i).inputValue()))
      await reasons.nth(i).fill("明确本次编辑的设定，保留旧版本供对照。");
  }
  await expect(save).toBeEnabled();
  await save.click();
}
