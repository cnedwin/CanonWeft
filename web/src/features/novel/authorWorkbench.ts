export type AuthorIntent =
  | "planning"
  | "characters"
  | "materials"
  | "write"
  | "batch"
  | "refine";
export type RefinementMode = "polish" | "shorten" | "expand" | "custom";
export interface RefinementScope {
  sourceText: string;
  start: number;
  end: number;
  quote: string;
}

export function workspaceStage(stage?: string) {
  switch (stage) {
    case "WORLD_PLANNING":
      return {
        title: "世界与故事",
        next: "完善并确认世界与故事",
        description: "先确定故事方向、人物与世界规则，再安排全书。",
      };
    case "OUTLINE_PLANNING":
      return {
        title: "全书与分卷大纲",
        next: "完善并确认全书大纲",
        description: "世界与故事已有确认版本，接下来安排主线、分卷与章节。",
      };
    case "DETAIL_PLANNING":
      return {
        title: "章节细纲与正文",
        next: "完善细纲，继续章节写作",
        description: "全书安排已确认；每章仍需确认细纲与有效依据后写作。",
      };
    default:
      return {
        title: "等待核对规划",
        next: "查看本书规划与确认状态",
        description: "确认状态以本书当前有效版本为准。",
      };
  }
}

export function captureRefinement(
  text: string,
  start: number,
  end: number,
): RefinementScope {
  if (
    !Number.isInteger(start) ||
    !Number.isInteger(end) ||
    start < 0 ||
    end > text.length ||
    end < start
  )
    throw new Error("选区位置已失效，请重新选择正文。");
  const quote = text.slice(start, end);
  if (!quote.trim()) throw new Error("请先选中需要精修的正文。");
  return { sourceText: text, start, end, quote };
}

export function refinementPrompt(
  scope: RefinementScope | null,
  mode: RefinementMode,
  instruction: string,
): string {
  if (mode === "custom" && !instruction.trim())
    throw new Error("请填写这次希望怎样修改。");
  const goals: Record<RefinementMode, string> = {
    polish: "润色：改善语句、动作与对白，保持原有叙事节奏。",
    shorten: "精简：删去赘述与重复信息，保留关键情节和必要因果。",
    expand: "扩写：补足具体动作、场景与情绪转折，不创造新的既定事实。",
    custom: "按作者要求修改。",
  };
  return `任务：${goals[mode]}\n范围：${scope ? "仅以下选区，选区之外逐字保持原样，标题和其他章节字段保持原样。" : "当前章正文，标题和其他章节字段保持原样。"}\n保留已确认的人物身份、主线、视角、因果、伏笔和事实边界。返回完整章节候选正文和修改理由，由作者比较后采用，不能自动采用。${instruction.trim() ? `\n作者要求：${instruction.trim()}` : ""}${scope ? `\n选区起止位置（UTF-16）：${scope.start}—${scope.end}\n选中原文：\n${scope.quote}\n选区结束。` : ""}`;
}

export function validateRefinement(
  scope: RefinementScope,
  currentText: string,
  candidateText: string,
): string {
  if (currentText !== scope.sourceText)
    throw new Error("正文已经变化，旧精修候选已失效，请重新选择范围。");
  const prefix = scope.sourceText.slice(0, scope.start);
  const suffix = scope.sourceText.slice(scope.end);
  if (
    !candidateText.startsWith(prefix) ||
    !candidateText.endsWith(suffix) ||
    candidateText.length < prefix.length + suffix.length
  )
    throw new Error(
      "候选修改了选区之外的正文，不能采用。请重新生成或选择整章精修。",
    );
  return candidateText.slice(
    prefix.length,
    candidateText.length - suffix.length,
  );
}
