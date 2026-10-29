import type { RecipeRun } from "../skills/recipeTypes";

export interface BookReviewFilters {
  query: string;
  severity: string;
  category: string;
  chapterId: string;
  disposition: string;
}
export interface BookReviewViewState {
  runId: string;
  filters: BookReviewFilters;
  focusedIssue: string;
}
export interface BookReviewEditTarget {
  run_id: string;
  step_id: string;
  output_hash: string;
  finding_id: string;
  chapter_id: string;
  quote: string;
  reviewed_hash: string;
  title: string;
  explanation: string;
  suggestion: string;
}
export interface BookReviewChapter {
  id: string;
  title: string;
  volume_id: string;
  volume: string;
  included: boolean;
  warning: string;
  hash?: string;
}
export interface BookReviewPreview {
  hash: string;
  included: number;
  missing: number;
  warnings: number;
  chapters: BookReviewChapter[];
  passes: number;
}
export interface BookReviewEvidence {
  chapter_id: string;
  quote: string;
}
export interface BookReviewFinding {
  id: string;
  category: string;
  severity: string;
  title: string;
  explanation: string;
  suggestion: string;
  evidence: BookReviewEvidence[];
}
export interface BookReviewPass {
  key: string;
  step_id?: string;
  output_hash?: string;
  chapter_ids: string[];
  result: {
    coverage: string[];
    checks: Record<string, string>;
    findings: BookReviewFinding[];
    carry: {
      key: string;
      category: string;
      summary: string;
      status: string;
      evidence: BookReviewEvidence[];
    }[];
    limits: string[];
  };
}
export interface BookReviewReport {
  run: RecipeRun;
  preview: BookReviewPreview;
  stale: boolean;
  stale_reason: string;
  passes: BookReviewPass[];
  covered: number;
  limits: string[];
}
export interface BookReviewChapterPin {
  chapter_id: string;
  resource_id: string;
  revision: number;
  hash: string;
  base_head: string;
  current_head: string;
  available: boolean;
  reason: string;
}
export type BookReviewDispositionStatus =
  | "open"
  | "revised"
  | "confirmed"
  | "deferred";
export interface BookReviewDisposition {
  source_step_id: string;
  source_output_hash: string;
  pass_key: string;
  finding_id: string;
  status: BookReviewDispositionStatus;
  note: string;
  chapters: BookReviewChapterPin[];
  stale: boolean;
  reason: string;
  operation_id?: string;
}
export interface BookReviewDispositions {
  resource_id: string;
  revision: number;
  hash: string;
  entries: BookReviewDisposition[];
  current_chapters: BookReviewChapterPin[];
}
export const bookReviewCategories: Record<string, string> = {
  causality: "因果与时空",
  character: "人物发展",
  foreshadow: "伏笔与兑现",
  theme: "主线与主题",
  highlight: "人物高光",
  language: "语言与细节",
};
export const bookReviewSeverities: Record<string, string> = {
  major: "优先修订",
  minor: "局部优化",
  question: "待作者确认",
};
export const bookReviewStatuses: Record<BookReviewDispositionStatus, string> = {
  open: "待处理",
  revised: "已修改，待复核",
  confirmed: "作者已人工核对",
  deferred: "暂缓处理",
};
export const bookReviewIssueKey = (
  run: string,
  step: string,
  hash: string,
  finding: string,
) => JSON.stringify([run, step, hash, finding]);
