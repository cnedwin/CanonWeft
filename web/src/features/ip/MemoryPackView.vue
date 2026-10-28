<script setup lang="ts">
import {
  memoryKinds,
  memoryStates,
  type MemoryItem,
  type MemoryPack,
  type MemoryResource,
  type MemorySource,
} from "./memory";
defineProps<{
  pack: MemoryPack;
  items: MemoryItem[];
  resources: MemoryResource[];
  stale: boolean;
  blocked: boolean;
}>();
const emit = defineEmits<{ source: [MemorySource] }>();
const channels: Record<string, string> = {
  control: "控制通道 · 隐藏创作约束",
  evidence: "表达通道 · 来源证据",
  unconfirmed: "未确认材料 · 不可作为正式事实",
  plan: "创作计划 · 尚未发生",
  reference: "参考材料 · 不作为故事事实",
};
const reasons: Record<string, string> = {
  required_or_dependency: "必需条目或其前置证据",
  ranked_or_dependency: "相关性选中或前置证据",
  dependency_unavailable: "前置证据不可用，未纳入",
  budget: "上下文容量不足，未纳入",
};
</script>
<template>
  <section class="pack-view" data-testid="memory-pack">
    <h3>本次上下文编译结果</h3>
    <p v-if="stale" class="notice" role="status">
      输入、权限范围或来源版本已变化，此结果仅供历史查看，请重新编译。
    </p>
    <p>
      纳入 {{ pack.items.length }} 条 · 保守估算
      {{ pack.estimated_tokens }} 个预算单位（不是模型的精确 token 数）
    </p>
    <p class="muted">
      编译仅整理上下文，不调用收费模型，不采用候选或发布正史。隐藏约束与可表达证据分别展示。
    </p>
    <article
      v-for="entry in pack.items"
      :key="entry.item.id"
      class="memory-card"
    >
      <strong>{{ channels[entry.channel] ?? entry.channel }}</strong>
      <p>
        {{ memoryKinds[entry.item.kind] }} ·
        {{ memoryStates[entry.item.state] }} ·
        {{ reasons[entry.reason] ?? entry.reason }}
      </p>
      <p class="text">{{ entry.item.text }}</p>
      <ul>
        <li v-for="(source, index) in entry.item.sources" :key="index">
          {{
            resources.find((r) => r.id === source.resource)?.name ??
            "来源资料不可用"
          }}
          · 第 {{ source.start_rune + 1 }}—{{ source.end_rune }} 字
          <button
            type="button"
            class="quiet"
            :disabled="blocked"
            @click="emit('source', source)"
          >
            查看已核对原文
          </button>
        </li>
      </ul>
    </article>
    <p v-if="!pack.items.length" class="muted">
      当前使用范围内没有可纳入的记忆。
    </p>
    <h4>未纳入的候选：共 {{ pack.omitted_count }} 条</h4>
    <p v-if="pack.omitted_count > pack.omissions.length" class="muted">
      以下仅列出 {{ pack.omissions.length }} 条可见遗漏样本，并非完整清单。
    </p>
    <ul v-if="pack.omissions.length">
      <li v-for="entry in pack.omissions" :key="entry.id">
        {{
          items.find((i) => i.id === entry.id)?.text.slice(0, 70) ??
          "当前不可显示的记忆"
        }}：{{ reasons[entry.reason] ?? entry.reason }}
      </li>
    </ul>
    <p v-else-if="!pack.omitted_count" class="muted">
      没有容量或前置依赖导致的遗漏。权限、会话和时间范围外的内容可能不会出现在结果中。
    </p>
  </section>
</template>
<style scoped>
.memory-card {
  border: 1px solid #d7ded5;
  border-radius: 8px;
  padding: 1rem;
  margin: 0.8rem 0;
}
.text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
