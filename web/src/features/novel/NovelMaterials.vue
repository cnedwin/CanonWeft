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
  <section class="novel-materials">
    <header class="materials-header">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>本书创作资料</h1>
        <p>导入要求、设定、原稿或专业参考，核对后再采用。</p>
      </div>
      <button
        :disabled="blocked || generationBlocked || loading"
        @click="emit('close')"
      >
        {{ returnLabel || "返回小说工作台" }}
      </button>
    </header>
    <p class="muted">
      原文件、解析结果和你的修改分别保留。未来剧情与故事规划请选择“项目工作资料”，
      避免成为角色已知事实。资料采用不等于小说规划已经确认，也不会自动生成正文。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <button v-if="error" :disabled="loading || blocked" @click="refresh">
      重试读取本书资料
    </button>
    <p v-if="loading" role="status">正在读取本书资料…</p>
    <BundlePanel
      v-if="branch"
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
    <section
      v-if="branch && repairPlan"
      ref="repairSection"
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
.novel-materials {
  max-width: 1100px;
  margin: auto;
}
.materials-header {
  display: flex;
  justify-content: space-between;
  gap: 20px;
  align-items: center;
  flex-wrap: wrap;
}
</style>
