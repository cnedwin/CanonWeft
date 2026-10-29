import type { RecipeRun } from "../skills/recipeTypes";
import { recipeStates } from "../skills/recipeTypes";

export type BatchFilter =
  | "all"
  | "unfinished"
  | "attention"
  | "completed"
  | "cancelled";

export function batchNeedsAttention(run: RecipeRun) {
  return (
    run.uncertain_calls > 0 ||
    ["BLOCKED", "FAILED", "UNKNOWN"].includes(run.state) ||
    run.restored_disabled
  );
}

export function batchUnfinished(run: RecipeRun) {
  return !["COMPLETED", "CANCELLED"].includes(run.state);
}

export function batchChapterTitles(
  run: RecipeRun,
  chapters: { id: string; title: string }[],
) {
  const ids = (
    run.frozen as RecipeRun["frozen"] & { input?: { chapter_ids?: string[] } }
  ).input?.chapter_ids;
  if (Array.isArray(ids) && ids.length)
    return ids.flatMap((id) => {
      const chapter = chapters.find((item) => item.id === id);
      return chapter ? [chapter.title] : [];
    });
  return (run.steps ?? [])
    .filter((step) => step.active)
    .flatMap((step) => {
      const input = step.frozen.input as typeof step.frozen.input & {
        novel_chapter?: { resource_id: string };
      };
      const chapter = chapters.find(
        (item) => item.id === input.novel_chapter?.resource_id,
      );
      return chapter ? [chapter.title] : [];
    });
}

export function filterBatches(
  runs: RecipeRun[],
  filter: BatchFilter,
  query: string,
  chapters: { id: string; title: string }[],
) {
  const needle = query.trim().toLocaleLowerCase();
  return runs.filter((run) => {
    if (filter === "unfinished" && !batchUnfinished(run)) return false;
    if (filter === "attention" && !batchNeedsAttention(run)) return false;
    if (filter === "completed" && run.state !== "COMPLETED") return false;
    if (filter === "cancelled" && run.state !== "CANCELLED") return false;
    return (
      !needle ||
      [
        run.id,
        recipeStates[run.state] ?? run.state,
        ...batchChapterTitles(run, chapters),
      ].some((text) => text.toLocaleLowerCase().includes(needle))
    );
  });
}

export function batchRecoveryAdvice(run: RecipeRun) {
  if (run.restored_disabled)
    return "此批次来自恢复副本，仅供核对。恢复不会重新授予执行资格，请核对版本、权限和费用后另行处理。";
  if (run.uncertain_calls > 0 || run.state === "UNKNOWN")
    return "先核对未知费用：已有请求结果尚未确认，占用仍保留。请在费用记录中对账，再处理失败章节；不要重复发送。";
  if (run.state === "COMPLETED")
    return "生成已完成。可逐章对照并加入私人稿，再打开精修；打开记录或预览不会自动采用、审稿或发布。";
  if (run.state === "CANCELLED")
    return "未完成部分已取消。已完成的候选和费用记录仍保留，取消不代表已发出请求的费用已释放。";
  if (run.state === "PREPARED")
    return "本批已准备，尚未开始。核对章节、模型及费用上限后，再明确确认开始。";
  if (run.state === "BLOCKED")
    return "本批需要处理。核对写作依据、模型和费用条件；条件恢复后再明确恢复，或针对失败章节建立新尝试。";
  if (run.state === "FAILED")
    return "本批未通过检查。保留原稿与失败记录，核对具体问题后再建立新尝试；不会自动覆盖正文或重新发送请求。";
  if (run.state === "WAITING_CONFIRMATION")
    return "本批正在等待明确确认。请先核对当前候选和费用，打开记录不会自动确认或继续执行。";
  if (run.paused)
    return "本批已暂停。核对当前进度和费用后可明确恢复；打开批次不会自行恢复执行。";
  return ["RUNNING", "WAITING", "PREPARING"].includes(run.state)
    ? "本批在后台推进。可查看进度或明确暂停、取消未完成部分；关闭工作台不会取消后台任务。"
    : "请刷新记录核对当前状态，再决定下一步；打开记录不会自动发出任务请求。";
}
