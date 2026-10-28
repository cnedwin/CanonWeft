export interface SemanticReview {
  retention?: { state: "EXPIRED"; expired_at: string };
  anchors?: { key: string; target_key: string; text: string }[];
  id: string;
  target_id: string;
  target_hash: string;
  state: string;
  verdict: string;
  reason: string;
  paused: boolean;
  cancel_requested: boolean;
  restored_disabled: boolean;
  generation_id: string;
  estimate_micros: number;
  estimate_currency: string;
  selection_revision: number;
  selected: boolean;
  selection_ready: boolean;
  frozen: { input_hash: string; scope: string; max_output_tokens: number };
  report?: {
    verdict: string;
    coverage: string[];
    summary: string;
    issues: {
      category: string;
      severity: string;
      target_key: string;
      explanation: string;
      anchor_keys: string[];
    }[];
  };
}

export const reviewStateNames: Record<string, string> = {
  PREPARED: "已准备，尚未调用",
  QUEUED: "等待执行",
  RUNNING: "正在独立审查",
  UNKNOWN: "请求结果未知，未重发",
  COMPLETED: "报告已生成",
  FAILED: "审查未通过执行检查",
  BLOCKED: "等待条件恢复",
  CANCELLED: "已取消",
};
export const reviewVerdictNames: Record<string, string> = {
  PENDING: "尚未审查",
  PASS: "所列范围未发现阻断问题",
  FAIL: "发现明确冲突",
  UNKNOWN: "依据不足，不能确定",
};
