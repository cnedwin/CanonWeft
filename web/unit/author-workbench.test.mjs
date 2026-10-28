import assert from "node:assert/strict";
import test from "node:test";
import {
  captureRefinement,
  refinementPrompt,
  validateRefinement,
  workspaceStage,
} from "../src/features/novel/authorWorkbench.ts";

test("阶段推荐服从服务端规划状态，未知状态不猜测可写章", () => {
  assert.match(workspaceStage("WORLD_PLANNING").next, /世界/);
  assert.match(workspaceStage("OUTLINE_PLANNING").next, /大纲/);
  assert.match(workspaceStage("DETAIL_PLANNING").next, /细纲/);
  assert.match(workspaceStage(undefined).next, /查看/);
});

test("选区保留真实位置和完整原文，不截断长段或猜重复文本", () => {
  const text = "😀同一句" + "长".repeat(2500) + "同一句尾声";
  const scope = captureRefinement(text, 2, 2505);
  assert.equal(scope.quote.length, 2503);
  assert.equal(scope.start, 2);
  assert.equal(scope.end, 2505);
  assert.throws(() => captureRefinement(text, 2, 2), /选中/);
  assert.throws(() => captureRefinement(text, -1, 4), /选区/);
});

test("选区精修拒绝范围外变化及手改后过期候选", () => {
  const text = "前文。同一句。后文。同一句。";
  const scope = captureRefinement(text, 3, 7);
  assert.equal(
    validateRefinement(scope, text, "前文。新的句子。后文。同一句。"),
    "新的句子。",
  );
  assert.throws(
    () => validateRefinement(scope, text, "前文。新的句子。后文也改了。"),
    /选区之外/,
  );
  assert.throws(
    () => validateRefinement(scope, "作者刚改过。", text),
    /已经变化/,
  );
});

test("精修提示明确模式与范围，按要求改缺少要求时不发起", () => {
  const scope = captureRefinement("前文。选段。后文。", 3, 6);
  assert.match(refinementPrompt(scope, "shorten", ""), /精简/);
  assert.match(refinementPrompt(scope, "expand", ""), /扩写/);
  assert.match(refinementPrompt(scope, "polish", ""), /润色/);
  assert.match(refinementPrompt(scope, "custom", "让语气克制"), /让语气克制/);
  assert.match(refinementPrompt(scope, "polish", ""), /选区之外逐字保持原样/);
  assert.throws(() => refinementPrompt(scope, "custom", " "), /填写/);
});
