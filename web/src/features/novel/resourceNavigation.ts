import type { DependencyRef } from "../ip/dependencies";

export interface ResourceNavigationRequest {
  ref: DependencyRef;
  title: string;
  explanation: string;
  focus_key: string;
}

export type NovelResourceTarget =
  | {
      kind: "planning";
      resource_id: string;
      source_revision: string;
      current_head: string;
    }
  | {
      kind: "chapter";
      resource_id: string;
      resource_kind: "control" | "body" | "ledger";
      chapter_id: string;
      control_id: string;
      ledger_id?: string;
      source_revision: string;
      current_head: string;
    };

interface ResourceMetadata {
  id: string;
  branch_id: string;
  name: string;
  kind: string;
  head: string;
  hash: string;
  revision: number;
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}
function identity(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value);
}
function version(value: unknown): value is string {
  return value === "" || identity(value);
}
function resourceMetadata(value: unknown): ResourceMetadata {
  if (
    !record(value) ||
    !identity(value.id) ||
    !identity(value.branch_id) ||
    typeof value.name !== "string" ||
    typeof value.kind !== "string" ||
    !version(value.head) ||
    typeof value.hash !== "string" ||
    (value.hash !== "" && !/^[0-9a-f]{64}$/.test(value.hash)) ||
    typeof value.revision !== "number" ||
    !Number.isSafeInteger(value.revision) ||
    value.revision < 1
  )
    throw new Error("当前资料目录不完整，请重新读取后再打开修改。");
  return {
    id: value.id,
    branch_id: value.branch_id,
    name: value.name,
    kind: value.kind,
    head: value.head,
    hash: value.hash,
    revision: value.revision,
  };
}

function planningIdentity(resource: ResourceMetadata): boolean {
  switch (resource.kind) {
    case "novel_plan_world":
      return resource.name === "世界与故事";
    case "novel_plan_book":
      return resource.name === "全书大纲";
    case "novel_plan_volume":
      return /^分卷章节安排 · [0-9a-f]{32}$/.test(resource.name);
    case "novel_plan_detail":
      return /^章节细纲 · [0-9a-f]{32}$/.test(resource.name);
    default:
      return false;
  }
}

function chapterIdentity(resource: ResourceMetadata): {
  kind: "control" | "body" | "ledger";
  chapter: string;
} | null {
  let kind: "control" | "body" | "ledger", prefix: string;
  switch (resource.kind) {
    case "novel_chapter_control":
      kind = "control";
      prefix = "章节工作稿 · ";
      break;
    case "novel_chapter_body":
      kind = "body";
      prefix = "章节正文 · ";
      break;
    case "novel_chapter_ledger":
      kind = "ledger";
      prefix = "剧情账本 · ";
      break;
    default:
      return null;
  }
  const chapter = resource.name.slice(prefix.length);
  if (!resource.name.startsWith(prefix) || !identity(chapter))
    throw new Error("此章节资料的稳定身份不完整，不能按标题猜测对应章稿。");
  return { kind, chapter };
}

export function resolveNovelResource(
  value: unknown,
  scope: { project_id: string; branch_id: string },
  ref: DependencyRef,
): NovelResourceTarget {
  if (
    !record(scope) ||
    !identity(scope.project_id) ||
    !identity(scope.branch_id) ||
    !record(ref) ||
    ref.kind !== "resource" ||
    !identity(ref.id) ||
    !identity(ref.branch_id) ||
    !version(ref.revision)
  )
    throw new Error("修改目标或小说范围不完整，请返回原清单重新读取。");
  if (ref.branch_id !== scope.branch_id)
    throw new Error("此资料属于其他分支，请从原分支的处理入口继续。");
  if (!record(value) || !Array.isArray(value.items))
    throw new Error("当前资料目录不可用，请重新读取后再打开修改。");

  // Resource metadata has no project_id field. The caller must obtain this
  // response from the authorized project/branch URL in scope; editors recheck
  // the selected resource against the actual book before displaying a draft.
  const resources = value.items.map(resourceMetadata);
  const ids = new Set<string>();
  for (const resource of resources) {
    if (resource.branch_id !== scope.branch_id)
      throw new Error("资料目录包含其他分支，未选择任何章稿或规划。");
    if (ids.has(resource.id))
      throw new Error("资料目录存在重复身份，不能确定修改目标。");
    ids.add(resource.id);
  }
  const resource = resources.find((item) => item.id === ref.id);
  if (!resource)
    throw new Error("目标资料已不在当前小说分支，请返回原清单重新检查。");
  const sameIdentity = resources.filter(
    (item) => item.kind === resource.kind && item.name === resource.name,
  );
  if (sameIdentity.length !== 1)
    throw new Error("相同稳定身份对应多份资料，未选择任何修改目标。");

  // DependencyRef.hash combines source bytes, policy and dependencies. It is
  // deliberately not compared with metadata.hash or exposed as a draft hash.
  const versions = {
    source_revision: ref.revision,
    current_head: resource.head,
  };
  if (planningIdentity(resource))
    return { kind: "planning", resource_id: resource.id, ...versions };
  const chapter = chapterIdentity(resource);
  if (!chapter)
    throw new Error(
      "此类资料不能直接进入小说章稿或规划，请使用原资料编辑入口。",
    );
  const controls = resources.filter(
    (item) =>
      item.kind === "novel_chapter_control" &&
      item.name === `章节工作稿 · ${chapter.chapter}`,
  );
  if (controls.length !== 1)
    throw new Error("未找到唯一的已有章节工作稿，不会自动创建或猜选章节。");
  return {
    kind: "chapter",
    resource_id: resource.id,
    resource_kind: chapter.kind,
    chapter_id: chapter.chapter,
    control_id: controls[0].id,
    ...(chapter.kind === "ledger" ? { ledger_id: resource.id } : {}),
    ...versions,
  };
}
