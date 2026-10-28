import { test, expect } from "@playwright/test";
import {
  groupedCharacterFields,
  characterFields,
} from "../src/features/ip/characterLibrary";
import { characterViewSlots } from "../src/features/ip/characterViews";
import type { Assertion } from "../src/features/ip/types";

test("角色资料分组保留全部常用属性、自定义属性和冲突值", () => {
  const base: Assertion = {
    id: "skill",
    subject: "hero",
    predicate: "skills",
    value: "医术",
    kind: "fact",
    effective_from: 0,
    known_from: 0,
    known_by: [],
    visibility: "public",
    recorded_at: "",
    locked: false,
    source: "作者",
  };
  const groups = groupedCharacterFields(
    [
      base,
      { ...base, id: "skill2", predicate: "技能", value: "剑术" },
      { ...base, id: "custom", predicate: "灵根", value: "木" },
      {
        ...base,
        id: "other",
        subject: "other",
        predicate: "other-only",
        value: "无",
      },
    ],
    "hero",
  );
  const fields = groups.flatMap((group) => group.fields);
  expect(fields.map((field) => field.name).sort()).toEqual(
    [...characterFields.map((field) => field[0]), "灵根"].sort(),
  );
  expect(
    groups
      .find((group) => group.name === "阵营与能力")
      ?.fields.find((field) => field.name === "技能")
      ?.values.map((value) => value.id),
  ).toEqual(["skill", "skill2"]);
  expect(
    groups.find((group) => group.name === "自定义属性")?.fields[0]?.values[0]
      ?.value,
  ).toBe("木");
});

test("人物目标与动机、恐惧与弱点分别展示，旧属性保留原意", () => {
  const values = {
    目标: "找到失踪的师父",
    动机: "偿还救命之恩",
    恐惧: "再次失去家人",
    弱点: "右手旧伤不能持重剑",
    服饰配色: "青白色",
    道具作用: "铜铃可感知附近阵法",
  };
  const facts: Assertion[] = Object.entries(values).map(
    ([predicate, value]) => ({
      id: predicate,
      subject: "hero",
      predicate,
      value,
      kind: "fact",
      effective_from: 0,
      known_from: 0,
      known_by: [],
      visibility: "public",
      recorded_at: "",
      locked: false,
      source: "作者",
    }),
  );
  const groups = groupedCharacterFields(facts, "hero");
  const fields = groups
    .filter((group) => group.name !== "自定义属性")
    .flatMap((group) => group.fields);
  for (const [name, value] of Object.entries(values)) {
    expect(
      fields.find((field) => field.name === name)?.values.map((v) => v.value),
    ).toEqual([value]);
  }
});

test("三视图只展示同角色同造型已采用的对应视角，组合图不冒充单视角", () => {
  const image = (
    id: string,
    view: string,
    published = true,
    look = "青衣",
    character = "hero",
  ) => ({
    id,
    published,
    report: { character_image: { view, look, character_id: character } },
  });
  const items = [
    image("f", "front"),
    image("f-old", "front"),
    image("pending", "side", false),
    image("other-look", "back", true, "盔甲"),
    image("other-character", "side", true, "青衣", "villain"),
    image("combined", "three_view"),
  ];
  const slots = characterViewSlots(items, "hero", "青衣");
  expect(slots.map((slot) => slot.images.map((item) => item.id))).toEqual([
    ["f", "f-old"],
    [],
    [],
  ]);
  expect(
    characterViewSlots(items, "hero", "").every(
      (slot) => slot.images.length === 0,
    ),
  ).toBe(true);
});
