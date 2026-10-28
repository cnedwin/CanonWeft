import type { Assertion, Entity, Snapshot } from "./types";

export const characterFields = [
  ["性别", "gender"],
  ["年龄", "年龄段", "age"],
  ["种族", "species"],
  ["角色定位", "定位"],
  ["标签", "tags"],
  ["背景", "经历", "background"],
  ["性格", "personality"],
  ["阵营", "组织", "faction"],
  ["职业", "身份", "profession"],
  ["技能", "能力", "skills"],
  ["代价", "能力代价"],
  ["限制", "能力限制"],
  ["服饰", "服装", "outfit"],
  ["服饰配色", "服装配色", "outfit_palette"],
  ["特色道具", "道具", "props"],
  ["道具作用", "道具功能", "prop_function"],
  ["外貌", "外貌特征", "appearance"],
  ["身高", "height"],
  ["体型"],
  ["辨识标志"],
  ["目标", "goal"],
  ["动机", "motivation"],
  ["恐惧", "fear"],
  ["弱点", "weakness"],
  ["价值观"],
  ["说话习惯", "口头禅"],
  ["人物弧线"],
  ["地位", "社会地位"],
  ["熟练度"],
  ["成长路径"],
  ["首次出场"],
  ["故事阶段"],
  ["面部特征"],
  ["发型"],
  ["发色"],
  ["眼睛", "瞳色"],
  ["肤色"],
  ["饰品"],
  ["姿态", "惯用动作"],
  ["惯用手", "handedness"],
  ["声线", "音色", "voice"],
  ["口音", "accent"],
  ["语速", "speaking_pace"],
  ["惯用表情", "表情特征", "expression"],
] as const;

export const characterFacets = [
  { key: "faction", name: "阵营", fields: ["阵营", "组织", "faction"] },
  { key: "profession", name: "职业", fields: ["职业", "身份", "profession"] },
  { key: "tags", name: "标签", fields: ["标签", "tags"] },
] as const;

const characterFieldGroups = [
  {
    name: "身份与定位",
    fields: [
      "性别",
      "年龄",
      "种族",
      "角色定位",
      "标签",
      "首次出场",
      "故事阶段",
    ],
  },
  {
    name: "经历与性格",
    fields: [
      "背景",
      "性格",
      "目标",
      "动机",
      "恐惧",
      "弱点",
      "价值观",
      "说话习惯",
      "人物弧线",
    ],
  },
  {
    name: "阵营与能力",
    fields: [
      "阵营",
      "职业",
      "地位",
      "技能",
      "熟练度",
      "代价",
      "限制",
      "成长路径",
    ],
  },
  {
    name: "外貌与装束",
    fields: [
      "外貌",
      "身高",
      "体型",
      "面部特征",
      "发型",
      "发色",
      "眼睛",
      "肤色",
      "辨识标志",
      "服饰",
      "服饰配色",
      "饰品",
      "特色道具",
      "道具作用",
      "姿态",
      "惯用手",
    ],
  },
  {
    name: "声音与表演",
    fields: ["声线", "口音", "语速", "惯用表情"],
  },
] as const;

export function groupedCharacterFields(facts: Assertion[], id: string) {
  const summaries = characterFieldSummaries(facts, id);
  const grouped = new Set<string>(
    characterFieldGroups.flatMap((group) => [...group.fields]),
  );
  return [
    ...characterFieldGroups.map((group) => ({
      name: group.name,
      fields: summaries.filter((field) =>
        (group.fields as readonly string[]).includes(field.name),
      ),
    })),
    {
      name: "自定义属性",
      fields: summaries.filter((field) => !grouped.has(field.name)),
    },
  ].filter((group) => group.fields.length > 0);
}

export type CharacterFacet = (typeof characterFacets)[number];

export function characterFacetValues(
  facts: Assertion[],
  id: string,
  facet: CharacterFacet,
): string[] {
  const values = characterValues(facts, id, facet.fields).flatMap((a) =>
    facet.key === "tags" ? a.value.split(/[,，;；、\n]+/) : [a.value],
  );
  return [
    ...new Set(values.map((v) => v.normalize("NFKC").trim()).filter(Boolean)),
  ];
}

export function characterFieldSummaries(facts: Assertion[], id: string) {
  const fields: readonly string[][] = characterFields.map((names) => [
    ...names,
  ]);
  const known = new Set(
    fields.flatMap((names) => names.map((n) => n.toLocaleLowerCase())),
  );
  const custom = [
    ...new Set(
      facts
        .filter(
          (a) =>
            a.subject === id &&
            !a.object_id &&
            !known.has(a.predicate.toLocaleLowerCase()),
        )
        .map((a) => a.predicate),
    ),
  ];
  return [...fields, ...custom.map((name) => [name])].map((names) => ({
    name: names[0]!,
    values: characterValues(facts, id, names),
  }));
}

// The author view uses the same reality/status/time semantics as activeFact in Go.
// It does not infer reciprocal relations or turn beliefs into canonical facts.
export function characterFacts(snapshot: Snapshot, at: number): Assertion[] {
  return (snapshot.assertions ?? []).filter(
    (a) =>
      a.kind === "fact" &&
      ["", "reality", "flashback"].includes(a.context ?? "") &&
      ["", "active"].includes(a.status ?? "") &&
      a.effective_from <= at &&
      (a.effective_until == null || at < a.effective_until),
  );
}

export function characterValues(
  facts: Assertion[],
  id: string,
  fields: readonly string[],
) {
  const names = fields.map((name) => name.toLocaleLowerCase());
  return facts.filter(
    (a) => a.subject === id && names.includes(a.predicate.toLocaleLowerCase()),
  );
}

export function characterMatches(
  entity: Entity,
  facts: Assertion[],
  query: string,
) {
  const searchable = [
    entity.id,
    entity.name,
    ...(entity.aliases ?? []),
    entity.description,
    ...facts
      .filter((a) => a.subject === entity.id)
      .flatMap((a) => [a.predicate, a.value]),
  ]
    .join(" ")
    .normalize("NFKC")
    .toLocaleLowerCase();
  return query
    .normalize("NFKC")
    .trim()
    .toLocaleLowerCase()
    .split(/\s+/)
    .every((word) => searchable.includes(word));
}

export function characterRelations(snapshot: Snapshot, at: number, id: string) {
  const ids = new Set(
    (snapshot.entities ?? [])
      .filter((e) => e.kind === "character")
      .map((e) => e.id),
  );
  return characterFacts(snapshot, at).filter(
    (a) =>
      a.object_id &&
      ids.has(a.subject) &&
      ids.has(a.object_id) &&
      (a.subject === id || a.object_id === id),
  );
}
