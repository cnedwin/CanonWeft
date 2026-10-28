import { test, expect } from "@playwright/test";

test("structured scope is sent, coverage is explicit, and changed query discards late result", async ({
  page,
}) => {
  const requests: Record<string, unknown>[] = [];
  let release: (() => void) | undefined;
  let delayed = false;
  await page.route("**/api/**", async (route) => {
    if (route.request().url().endsWith("/search")) {
      const input = route.request().postDataJSON() as Record<string, unknown>;
      requests.push(input);
      if (delayed)
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      await route.fulfill({
        json: {
          id: "context-1",
          index_id: "index-1",
          ip_head: "canon-1",
          input,
          facts: [],
          hidden_constraints: [],
          evidence: [],
          omissions: [],
          estimated_tokens: 5000,
          token_budget: 65536,
          structured_selection: {
            version: "structured-context-v1",
            selected_assertions: 2,
            omitted_assertions: 180,
            selected_entities: 1,
            omitted_entities: 70,
            required_assertions: 1,
            required_entities: 1,
          },
        },
      });
    } else await route.fulfill({ json: { items: [], runtime_ready: true } });
  });
  await page.goto("/tests/fixtures/structured-context.html");
  await page.getByLabel("要查找的线索").fill("林舟寻找线索");
  await page.getByText("指定本次涉及的设定", { exact: true }).click();
  await page.getByLabel("本次必须带入的人物与实体").selectOption(["hero"]);
  await page.getByLabel("本次必须带入的命题").selectOption(["identity"]);
  await page
    .getByRole("button", { name: "查找并保存证据包", exact: true })
    .click();
  await expect(page.getByTestId("structured-coverage")).toContainText(
    "180 条命题",
  );
  expect(requests[0]?.structured_scope).toEqual({
    focus_entity_ids: ["hero"],
    required_assertion_ids: ["identity"],
  });
  await page.getByLabel("本次必须带入的命题").selectOption([]);
  await expect(page.getByTestId("structured-coverage")).toHaveCount(0);
  delayed = true;
  await page
    .getByRole("button", { name: "查找并保存证据包", exact: true })
    .click();
  await expect.poll(() => requests.length).toBe(2);
  await page.getByLabel("要查找的线索").fill("另一个任务");
  release?.();
  await expect(
    page.getByRole("button", { name: "查找并保存证据包", exact: true }),
  ).toBeEnabled();
  await expect(page.getByTestId("structured-coverage")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "用于下方 AI 候选任务" }),
  ).toHaveCount(0);
});
