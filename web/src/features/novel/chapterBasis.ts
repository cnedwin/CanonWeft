import type { DependencyRef } from "../ip/dependencies";
import {
  planFields,
  sceneFields,
  type PlanningDocument,
  type PlannedScene,
} from "./planningTypes";

export interface ChapterBasisSnapshot {
  detail_ref: DependencyRef;
  detail: PlanningDocument | null;
  settings_revision: number;
  settings_hash: string;
  ip_head: string;
}
export interface ChapterBasisPreview {
  resource_id: string;
  chapter_id: string;
  revision: number;
  hash: string;
  base_head: string;
  current_head: string;
  basis_hash: string;
  can_refresh: boolean;
  reason: string;
  before: ChapterBasisSnapshot;
  after: ChapterBasisSnapshot;
  settings_changes: {
    field: string;
    label: string;
    scope: string;
    before: unknown;
    after: unknown;
  }[];
  ip_changes: {
    ref: DependencyRef;
    name: string;
    fields: { name: string; before: unknown; after: unknown }[];
  }[];
  limitations: string[];
}
export interface BasisDifference {
  label: string;
  before: string;
  after: string;
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}
function ref(value: unknown): value is DependencyRef {
  return (
    record(value) &&
    ["kind", "branch_id", "id", "revision", "hash"].every(
      (key) => typeof value[key] === "string",
    )
  );
}
function detail(value: unknown): value is PlanningDocument | null {
  if (value === null) return true;
  return (
    record(value) &&
    value.kind === "detail" &&
    typeof value.scope === "string" &&
    Number.isSafeInteger(value.settings_revision) &&
    Array.isArray(value.inputs) &&
    (value.scenes === null ||
      (Array.isArray(value.scenes) &&
        value.scenes.every(
          (scene) =>
            record(scene) &&
            typeof scene.id === "string" &&
            typeof scene.words === "number" &&
            sceneFields.every((field) => typeof scene[field.key] === "string"),
        ))) &&
    planFields
      .filter((field) => field.kinds.includes("detail"))
      .every((field) => typeof value[field.key] === "string")
  );
}
function snapshot(value: unknown): value is ChapterBasisSnapshot {
  return (
    record(value) &&
    ref(value.detail_ref) &&
    detail(value.detail) &&
    Number.isSafeInteger(value.settings_revision) &&
    typeof value.settings_hash === "string" &&
    typeof value.ip_head === "string"
  );
}
export function validBasisPreview(
  value: unknown,
): value is ChapterBasisPreview {
  return (
    record(value) &&
    [
      "resource_id",
      "chapter_id",
      "hash",
      "base_head",
      "current_head",
      "basis_hash",
      "reason",
    ].every((key) => typeof value[key] === "string") &&
    !!value.resource_id &&
    !!value.chapter_id &&
    !!value.basis_hash &&
    Number.isSafeInteger(value.revision) &&
    Number(value.revision) >= 0 &&
    typeof value.can_refresh === "boolean" &&
    snapshot(value.before) &&
    snapshot(value.after) &&
    Array.isArray(value.limitations) &&
    value.limitations.every((item) => typeof item === "string") &&
    Array.isArray(value.settings_changes) &&
    value.settings_changes.every(
      (item) =>
        record(item) &&
        ["field", "label", "scope"].every(
          (key) => typeof item[key] === "string",
        ) &&
        Object.hasOwn(item, "before") &&
        Object.hasOwn(item, "after"),
    ) &&
    Array.isArray(value.ip_changes) &&
    value.ip_changes.every(
      (item) =>
        record(item) &&
        ref(item.ref) &&
        typeof item.name === "string" &&
        Array.isArray(item.fields) &&
        item.fields.every(
          (field) =>
            record(field) &&
            typeof field.name === "string" &&
            Object.hasOwn(field, "before") &&
            Object.hasOwn(field, "after"),
        ),
    )
  );
}

export function basisValue(value: unknown): string {
  if (value === null || value === undefined || value === "") return "未指定";
  if (typeof value === "string") {
    // IP field differences may contain serialized JSON objects or tag lists.
    try {
      const parsed: unknown = JSON.parse(value);
      if (parsed !== null && typeof parsed === "object")
        return JSON.stringify(parsed, null, 2);
    } catch {
      /* Plain text is the usual field representation. */
    }
    return value;
  }
  return typeof value === "object"
    ? JSON.stringify(value, null, 2)
    : String(value);
}
function sceneValue(scene: PlannedScene): string {
  return [
    ...sceneFields.map(
      (field) => `${field.label}：${basisValue(scene[field.key])}`,
    ),
    `计划字数：${scene.words}`,
  ].join("\n");
}
export function detailDifferences(
  before: PlanningDocument | null,
  after: PlanningDocument | null,
): BasisDifference[] {
  if (!before || !after) return [];
  const rows: BasisDifference[] = [];
  for (const field of planFields.filter((item) =>
    item.kinds.includes("detail"),
  )) {
    if (before[field.key] !== after[field.key])
      rows.push({
        label: field.label,
        before: basisValue(before[field.key]),
        after: basisValue(after[field.key]),
      });
  }
  const prior = new Map(
    (before.scenes ?? []).map((scene, index) => [scene.id, { scene, index }]),
  );
  const next = new Map(
    (after.scenes ?? []).map((scene, index) => [scene.id, { scene, index }]),
  );
  const beforeRetained = [...prior.keys()].filter((id) => next.has(id));
  const afterRetained = [...next.keys()].filter((id) => prior.has(id));
  for (const [id, current] of next) {
    const old = prior.get(id);
    const label = `场景 ${current.index + 1}`;
    if (!old) {
      rows.push({
        label: `新增${label}`,
        before: "旧细纲无此场景",
        after: sceneValue(current.scene),
      });
      continue;
    }
    if (beforeRetained.indexOf(id) !== afterRetained.indexOf(id))
      rows.push({
        label: `${label} · 次序`,
        before: `场景 ${old.index + 1}`,
        after: label,
      });
    for (const field of sceneFields) {
      if (old.scene[field.key] !== current.scene[field.key])
        rows.push({
          label: `${label} · ${field.label}`,
          before: basisValue(old.scene[field.key]),
          after: basisValue(current.scene[field.key]),
        });
    }
    if (old.scene.words !== current.scene.words)
      rows.push({
        label: `${label} · 计划字数`,
        before: String(old.scene.words),
        after: String(current.scene.words),
      });
  }
  for (const [id, old] of prior)
    if (!next.has(id))
      rows.push({
        label: `移出场景 ${old.index + 1}`,
        before: sceneValue(old.scene),
        after: "当前细纲已无此场景",
      });
  return rows;
}
