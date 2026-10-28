export interface OriginSelection {
  generation_id?: string;
  parent_release_id?: string;
}

export interface ArtifactOrigin {
  kind: "manual" | "import" | "generation" | "derived" | "recovered";
  actor_id: string;
  resource_id: string;
  branch_id: string;
  base_head: string;
  ip_head: string;
  content_hash: string;
  parent_release_id?: string;
  parent_hash?: string;
  import_id?: string;
  import_hash?: string;
  review_required: boolean;
  recovery?: {
    project_id: string;
    branch_id: string;
    release_id: string;
    resource_id: string;
    manifest_hash: string;
    content_hash: string;
    origin_hash: string;
    origin_known: boolean;
    generation_hash?: string;
  }[];
  generation?: {
    generation_id: string;
    call_id: string;
    input_hash: string;
    output_hash: string;
    text_hash: string;
    role: string;
    config_id: string;
    config_revision: number;
    config_hash: string;
    mode: string;
    agent_version_id?: string;
    manifest_hash?: string;
    binding_id?: string;
    qualification_id?: string;
    task_kind?: string;
    suite_hash?: string;
  };
}

export const originNames: Record<ArtifactOrigin["kind"], string> = {
  manual: "人工整理的参考资料",
  import: "导入文件资料",
  generation: "固定生成结果",
  derived: "基于已有来源修改",
  recovered: "恢复的历史资料（原始来源未记录）",
};
