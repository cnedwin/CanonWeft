import { expect, test } from "@playwright/test";
import {
  compareProse,
  type ProseComparison,
} from "../src/features/novel/proseComparison";

function expectReconstruction(
  result: ProseComparison,
  left: string,
  right: string,
) {
  expect(
    result.blocks
      .filter((block) => block.kind !== "added")
      .map((block) => block.text)
      .join(""),
  ).toBe(left);
  expect(
    result.blocks
      .filter((block) => block.kind !== "removed")
      .map((block) => block.text)
      .join(""),
  ).toBe(right);
  expect(result.equal).toBe(left === right);
  expect(result.added).toBe(
    result.blocks
      .filter((block) => block.kind === "added")
      .reduce((total, block) => total + Array.from(block.text).length, 0),
  );
  expect(result.removed).toBe(
    result.blocks
      .filter((block) => block.kind === "removed")
      .reduce((total, block) => total + Array.from(block.text).length, 0),
  );
}

test("正文完全相同只返回原始 same 块，包括空正文", () => {
  for (const text of ["", "普通正文", "  \t\r\n", "序章😀\n尾行\n"]) {
    const result = compareProse(text, text);
    expect(result).toEqual({
      equal: true,
      coarse: false,
      added: 0,
      removed: 0,
      blocks: [{ kind: "same", text }],
    });
    expectReconstruction(result, text, text);
  }
});

test("Unicode、组合字符、不同换行与尾换行都可精确重建", () => {
  const examples = [
    ["序😀\r\n甲 e\u0301\r乙\u2028末\u2029", "序😀\r\n甲 é\r乙\u2028末\u2029"],
    ["前文\n尾😀", "前文\n尾😀\n"],
    ["\ufeff前文\r\n\t  \n", "\ufeff前文\n\t \r\n"],
    ["", "😀"],
    ["😀", ""],
  ];
  for (const [left, right] of examples) {
    const result = compareProse(left, right);
    expect(result.coarse).toBe(false);
    expectReconstruction(result, left!, right!);
  }
  expect(compareProse("😀", "🧭")).toMatchObject({ added: 1, removed: 1 });
});

test("单纯空格、制表符和换行变化不能被规范化成相同", () => {
  for (const [left, right] of [
    ["甲 乙\n", "甲乙\n"],
    ["甲\t乙", "甲 乙"],
    ["甲\n", "甲\r\n"],
    ["甲", "甲\n"],
    ["\n", ""],
  ]) {
    const result = compareProse(left!, right!);
    expect(result.equal).toBe(false);
    expect(result.blocks.some((block) => block.kind !== "same")).toBe(true);
    expectReconstruction(result, left!, right!);
  }
});

test("重复行按顺序匹配且保留中间共同正文", () => {
  const left = "开头\n重复\n重复\n旧句\n共同\n结尾\n";
  const right = "开头\n重复\n新句\n重复\n共同\n结尾\n";
  const result = compareProse(left, right);
  expect(result.coarse).toBe(false);
  expect(result.blocks.some((block) => block.kind === "removed")).toBe(true);
  expect(result.blocks.some((block) => block.kind === "added")).toBe(true);
  expect(result.blocks.filter((block) => block.kind === "same")).toContainEqual(
    {
      kind: "same",
      text: "重复\n",
    },
  );
  expectReconstruction(result, left, right);
  expect(compareProse(left, right)).toEqual(result);
});

test("中间 LCS 矩阵超过上限时保留首尾并明确回退为大块差异", () => {
  const leftMiddle = Array.from({ length: 450 }, (_, i) => `原句${i}\n`).join(
    "",
  );
  const rightMiddle = Array.from({ length: 450 }, (_, i) => `新句${i}\n`).join(
    "",
  );
  const left = `共同开头\n${leftMiddle}共同结尾\n`;
  const right = `共同开头\n${rightMiddle}共同结尾\n`;
  const result = compareProse(left, right);
  expect(result.coarse).toBe(true);
  expect(result.blocks).toEqual([
    { kind: "same", text: "共同开头\n" },
    { kind: "removed", text: leftMiddle },
    { kind: "added", text: rightMiddle },
    { kind: "same", text: "共同结尾\n" },
  ]);
  expectReconstruction(result, left, right);
});

test("单侧超过行数上限也回退，避免狭长矩阵无限增长", () => {
  const left = Array.from({ length: 1001 }, (_, i) => `行${i}\n`).join("");
  const right = "替代内容\n";
  const result = compareProse(left, right);
  expect(result.coarse).toBe(true);
  expect(result.blocks.map((block) => block.kind)).toEqual([
    "removed",
    "added",
  ]);
  expectReconstruction(result, left, right);
});

test("32 KiB 以上长篇仅比较变化中段，大范围变化有界回退", () => {
  const opening = "长篇共同开场与人物对白😀。\n".repeat(3000);
  const ending = "长篇共同结尾与后记。\n".repeat(3000);
  const left = `${opening}旧的转折\n${ending}`;
  const right = `${opening}新的转折\n${ending}`;
  expect(new TextEncoder().encode(left).byteLength).toBeGreaterThan(32768);
  const started = performance.now();
  const result = compareProse(left, right);
  expect(result.coarse).toBe(false);
  expect(result.blocks).toHaveLength(4);
  expectReconstruction(result, left, right);
  const changedLeft = "原文甲乙丙丁。\n".repeat(6000);
  const changedRight = "改文子丑寅卯。\n".repeat(6000);
  const fallback = compareProse(changedLeft, changedRight);
  expect(fallback.coarse).toBe(true);
  expect(fallback.blocks).toHaveLength(2);
  expectReconstruction(fallback, changedLeft, changedRight);
  // A generous regression bound catches whole-book quadratic work without
  // making this a device-specific rendering or literary-quality benchmark.
  expect(performance.now() - started).toBeLessThan(2000);
});
