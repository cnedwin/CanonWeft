import type { CapabilityVersion } from "./capabilityTypes";
export interface RecipeRoute {
  target_pointer: string;
  source_step: string;
  source_pointer: string;
}
export interface RecipeStageInput {
  key: string;
  price_id: string;
  max_output_tokens: number;
  context_id: string;
  binding_id: string;
  task_kind: string;
  input?: unknown;
  input_text?: string;
  bindings: RecipeRoute[];
}
export interface RecipeStage {
  id: string;
  key: string;
  ordinal: number;
  attempt: number;
  active: boolean;
  generation_id: string;
  state: string;
  input_hash: string;
  output_hash: string;
  confirmed_hash: string;
  output?: unknown;
  input_text?: string;
  output_text?: string;
  frozen: {
    input: RecipeStageInput;
    agent: CapabilityVersion;
    step: { wait_for_confirmation: boolean };
    config: { name: string; model: string };
  };
}
export interface RecipeRun {
  retention?: { state: "EXPIRED"; expired_at: string };
  id: string;
  branch_id: string;
  state: string;
  revision: number;
  paused: boolean;
  cancel_requested: boolean;
  restored_disabled: boolean;
  reason: string;
  currency: string;
  budget_limit_micros: number;
  spent_micros: number;
  held_micros: number;
  uncertain_calls: number;
  frozen: { definition: CapabilityVersion; trial: boolean };
  steps?: RecipeStage[];
}
export const recipeStates: Record<string, string> = {
  PREPARED: "已准备，尚未调用",
  RUNNING: "执行中",
  WAITING: "等待前序步骤",
  PREPARING: "准备阶段输入",
  WAITING_CONFIRMATION: "等待作者确认",
  COMPLETED: "已完成",
  FAILED: "未通过检查",
  UNKNOWN: "结果未知，请先核对账单",
  BLOCKED: "等待条件恢复",
  CANCELLED: "已取消",
};
