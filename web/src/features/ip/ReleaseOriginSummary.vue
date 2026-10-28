<script setup lang="ts">
import { originNames, type ArtifactOrigin } from "./releaseOrigins";
defineProps<{ origin?: ArtifactOrigin; legacy?: boolean }>();
</script>

<template>
  <section class="origin-summary" aria-label="候选来源">
    <template v-if="origin">
      <p>
        <strong>来源：{{ originNames[origin.kind] }}</strong>
      </p>
      <p v-if="origin.recovery?.length" class="notice">
        从旧作品恢复，来源记录仅用于追溯。旧生成任务、调用授权和审核资格不会恢复。
      </p>
      <p v-if="origin.review_required" class="notice">
        旧资格对应旧内容，当前修改需要独立复核；若来源尚未取得资格，也不能用于获准发布。
        候选和草稿会保留，当前不能确认采用或正式发布。
      </p>
      <p v-else-if="origin.generation">
        已固定生成结果及方法资格，采用与发布时会重新检查。此记录不代表小说专业质量验收。
      </p>
      <p v-else>
        按参考资料记录来源；没有将人工整理或文件导入标为 AI 审核通过。
      </p>
      <dl v-if="origin.generation">
        <dt>生成任务</dt>
        <dd>{{ origin.generation.generation_id }}</dd>
        <dt>用途</dt>
        <dd>{{ origin.generation.task_kind || origin.generation.role }}</dd>
        <dt>执行记录</dt>
        <dd>{{ origin.generation.mode }}</dd>
        <dt v-if="origin.generation.qualification_id">资格记录</dt>
        <dd v-if="origin.generation.qualification_id">
          {{ origin.generation.qualification_id }}
        </dd>
      </dl>
      <details>
        <summary>查看来源版本与内容指纹</summary>
        <dl>
          <dt>本次内容</dt>
          <dd>{{ origin.content_hash }}</dd>
          <dt>目标资料</dt>
          <dd>{{ origin.resource_id }}</dd>
          <dt>基础资料版本</dt>
          <dd>{{ origin.base_head || "首次候选" }}</dd>
          <dt v-if="origin.parent_release_id">来源联合版本</dt>
          <dd v-if="origin.parent_release_id">
            {{ origin.parent_release_id }}
          </dd>
          <dt v-if="origin.import_id">导入记录</dt>
          <dd v-if="origin.import_id">{{ origin.import_id }}</dd>
          <dt v-if="origin.import_hash">原文件指纹</dt>
          <dd v-if="origin.import_hash">{{ origin.import_hash }}</dd>
          <template v-if="origin.generation">
            <dt>模型配置版本</dt>
            <dd>
              {{ origin.generation.config_id }} /
              {{ origin.generation.config_revision }}
            </dd>
            <dt>固定能力版本</dt>
            <dd>
              {{ origin.generation.agent_version_id || "未绑定专业能力" }}
            </dd>
            <dt>能力清单</dt>
            <dd>{{ origin.generation.manifest_hash || "无" }}</dd>
            <dt>原始输出指纹</dt>
            <dd>{{ origin.generation.text_hash }}</dd>
            <dt>请求指纹</dt>
            <dd>{{ origin.generation.input_hash }}</dd>
          </template>
        </dl>
      </details>
    </template>
    <p v-else-if="legacy" class="notice">
      旧版清单没有完整来源记录，仅供查看；请基于当前资料保存新候选。
    </p>
  </section>
</template>

<style scoped>
.origin-summary {
  margin: 0.8rem 0;
}
dt {
  font-weight: 600;
}
dd {
  margin: 0.2rem 0 0.6rem;
  overflow-wrap: anywhere;
}
</style>
