import assert from "node:assert/strict";
import test from "node:test";
import { locateMemorySource } from "../src/features/ip/memory.ts";

const resource = {
  id: "resource-1",
  name: "人物前史",
  head: "revision-1",
  hash: "a".repeat(64),
  text: "序😀林舟守住承诺。尾声",
};
test("记忆来源按 Unicode 码点定位，绑定已确认资源版本和哈希", () => {
  assert.deepEqual(locateMemorySource(resource, "林舟守住承诺。"), {
    resource: resource.id,
    revision: resource.head,
    hash: resource.hash,
    start_rune: 2,
    end_rune: 9,
  });
});
test("相同原文出现两次时拒绝猜测位置", () => {
  assert.throws(
    () => locateMemorySource({ ...resource, text: "承诺与承诺" }, "承诺"),
    /出现多次/,
  );
});
test("拒绝未确认资源、空片段及不存在的原文", () => {
  assert.throws(
    () => locateMemorySource({ ...resource, head: "" }, "林舟"),
    /已确认/,
  );
  assert.throws(() => locateMemorySource(resource, ""), /片段/);
  assert.throws(() => locateMemorySource(resource, "另一本书"), /找不到/);
});
