<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { api, APIError, message, type Actor, type Project } from "../../api";
import type { NovelBook } from "./types";
import type { Branch, Candidate } from "../ip/types";
import BundlePanel from "../ip/BundlePanel.vue";
import GenerationPanel from "../ip/GenerationPanel.vue";

const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
  returnLabel?: string;
}>();
const emit = defineEmits<{ close: []; expired: [] }>();
const branch = ref<Branch | null>(null);
const candidates = ref<Candidate[]>([]);
const loading = ref(false);
const blocked = ref(false);
const generationBlocked = ref(false);
const bundle = ref<InstanceType<typeof BundlePanel> | null>(null);
const repairSection = ref<HTMLElement | null>(null);
const repairPlan = ref<{ id: string; branch: string } | null>(null);
const error = ref("");
let disposed = false;
async function useRepairPlan(plan: { id: string; branch: string }) {
  if (plan.branch !== branch.value?.id) return;
  repairPlan.value = plan;
  await nextTick();
  if (!disposed) repairSection.value?.focus();
}
async function refresh() {
  if (loading.value) return;
  loading.value = true;
  error.value = "";
  try {
    const root = `/projects/${props.project.id}/branches/${props.book.branch_id}`;
    const [current, history] = await Promise.all([
      api<Branch>(root),
      api<{ items: Candidate[] }>(`${root}/candidates`),
    ]);
    if (disposed) return;
    branch.value = current;
    candidates.value = history.items;
  } catch (e) {
    if (disposed) return;
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (!disposed) loading.value = false;
  }
}
onMounted(refresh);
onBeforeUnmount(() => {
  disposed = true;
});
</script>

<template>
  <section class="novel-materials author-subpage" :aria-busy="loading">
    <header class="materials-header author-page-header">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>本书创作资料</h1>
        <p class="author-page-description">
          把要求、设定、原稿与专业参考整理成本书可引用的资料，核对后再采用。
        </p>
      </div>
      <button
        class="quiet"
        :disabled="blocked || generationBlocked || loading"
        @click="emit('close')"
      >
        {{ returnLabel || "返回小说工作台" }}
      </button>
    </header>
    <ol class="materials-workflow" aria-label="本书资料整理流程">
      <li>
        <strong>1 · 导入资料</strong
        ><span>上传原文件，保留来源与解析结果。</span>
      </li>
      <li>
        <strong>2 · 核对内容</strong
        ><span>修订解析文字，检查候选与引用范围。</span>
      </li>
      <li>
        <strong>3 · 采用并发布</strong
        ><span>明确采用核对过的版本，供后续创作引用。</span>
      </li>
    </ol>
    <p class="author-status">
      未来剧情与故事规划请选择“项目工作资料”，避免成为角色已知事实。
    </p>
    <details class="materials-guidance">
      <summary>资料保留与后续写作说明</summary>
      <p class="muted">
        原文件、解析结果和你的修改分别保留。资料采用不等于小说规划已经确认，也不会自动生成正文。
      </p>
    </details>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <button
      v-if="error"
      class="quiet"
      :disabled="loading || blocked"
      @click="refresh"
    >
      重试读取本书资料
    </button>
    <p v-if="loading" role="status" class="author-status">正在读取本书资料…</p>
    <section
      v-if="branch"
      class="author-work-area materials-content"
      aria-label="资料导入与核对"
    >
      <BundlePanel
        ref="bundle"
        :key="branch.id"
        :project="project"
        :actor="actor"
        :branch="branch"
        :candidates="candidates"
        :blocked="loading"
        initially-open-imports
        @blocking="blocked = $event"
        @expired="emit('expired')"
        @published="refresh"
        @change-plan="useRepairPlan"
      />
    </section>
    <section
      v-if="branch && repairPlan"
      ref="repairSection"
      class="author-work-area"
      tabindex="-1"
      aria-label="资料变更修复"
    >
      <GenerationPanel
        :project="project"
        :actor="actor"
        :branch="branch"
        :change-plan="repairPlan"
        @blocking="generationBlocked = $event"
        @expired="emit('expired')"
        @prepared="bundle?.refreshAfterPrepared()"
      />
    </section>
  </section>
</template>

<style scoped>
.materials-workflow {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  list-style: none;
  padding: 0;
  margin: 20px 0;
}
.materials-workflow li {
  display: grid;
  gap: 8px;
  padding: 16px;
  border: 1px solid #d7ded5;
  border-radius: 8px;
  background: #fafaf6;
}
.materials-workflow span {
  color: #5d6e63;
  font-size: 14px;
  line-height: 1.7;
}
.materials-guidance {
  padding: 10px 0;
}
.materials-content :deep(.bundle-panel) {
  margin-top: 0;
}
.materials-content :deep(.bundle-panel > details) {
  padding: 12px 0;
  border-top: 1px solid #d7ded5;
}
.materials-content :deep(.bundle-panel > .create button) {
  background: transparent;
  color: #52665d;
  border: 1px solid #d7ded5;
}
.materials-content :deep(.bundle-panel > .create button:hover) {
  background: #edf1e6;
}
@media (max-width: 700px) {
  .materials-workflow {
    grid-template-columns: 1fr;
  }
}
</style>
