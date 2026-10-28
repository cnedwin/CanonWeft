export interface CapabilityRef {
  id: string;
  hash: string;
  kind: string;
}
export interface CapabilityDefinition {
  kind: string;
  key: string;
  name: string;
  description: string;
  instructions: string;
  input_schema: Record<string, unknown>;
  output_schema: Record<string, unknown>;
  dependencies: CapabilityRef[];
  role?: string;
  package_id?: string;
  package_hash?: string;
  tool?: {
    runtime: string;
    entrypoint: string;
    timeout_seconds: number;
    memory_mib: number;
    network: boolean;
  };
  steps?: {
    key: string;
    agent_id: string;
    after: string[];
    wait_for_confirmation: boolean;
  }[];
  max_automatic_revisions?: number;
}
export interface CapabilityVersion {
  retention?: { state: "EXPIRED"; expired_at: string };
  id: string;
  hash: string;
  parent_id: string;
  created_at: string;
  definition: CapabilityDefinition;
  input_schema_text: string;
  output_schema_text: string;
}
export interface CapabilityManifest {
  root: CapabilityRef;
  hash: string;
  versions: CapabilityVersion[];
  trial_ready: boolean;
  qualified: boolean;
  findings: { version_id: string; code: string; message: string }[];
}
export const capabilityKinds: Record<string, string> = {
  agent: "专业角色",
  skill: "方法",
  tool: "执行工具",
  recipe: "工作流",
};
export const capabilityRoles: Record<string, string> = {
  architect: "世界与故事设计",
  planner: "故事规划",
  writer: "正文创作",
  bookkeeper: "独立记账",
  critic: "独立审阅",
  researcher: "资料研究",
  character_designer: "角色设计",
  continuity_editor: "连续性修订",
  style_editor: "文风编辑",
  asset_designer: "形象设计",
  import_reader: "导入整理",
};
