<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import type { Branch, Candidate } from "./types";
interface Task {
  id: string;
  branch_id: string;
  candidate_id: string;
  state: string;
  reason: string;
  paused: boolean;
  cancelled: boolean;
  revision: number;
  input_hash: string;
}
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: Branch;
  candidates: Candidate[];
  blocked: boolean;
}>();
const emit = defineEmits<{ expired: [] }>();
const tasks = ref<Task[]>([]);
const runtimeReady = ref(false);
const candidate = ref("");
const operation = ref(newOperationID());
function changed() {
  operation.value = newOperationID();
}
const busy = ref(false);
const refreshing = ref(false);
const error = ref("");
let disposed = false;
let timer: ReturnType<typeof setInterval> | undefined;
const root = `/projects/${props.project.id}/tasks`;
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const available = computed(() =>
  props.candidates.filter(
    (c) => c.status !== "PUBLISHED" && c.base_head === props.branch.head,
  ),
);
const names: Record<string, string> = {
  QUEUED: "等待调度",
  WAITING_ADOPTION: "等待确认采用",
  WAITING_PUBLICATION: "等待正式发布",
  PAUSED: "已暂停",
  CANCELLED: "已取消",
  STALE: "基础版本已变化",
  BLOCKED: "需要处理",
  COMPLETED: "已完成",
};
const search = ref("");
const statusFilter = ref("all");
const needsAttention = (task: Task) =>
  ["WAITING_ADOPTION", "WAITING_PUBLICATION", "BLOCKED", "STALE"].includes(
    task.state,
  );
const attentionCount = computed(
  () => tasks.value.filter(needsAttention).length,
);
const visibleTasks = computed(() => {
  const query = search.value.trim().toLocaleLowerCase();
  return tasks.value.filter((task) => {
    const matchesStatus =
      statusFilter.value === "all" ||
      (statusFilter.value === "attention" && needsAttention(task)) ||
      (statusFilter.value === "paused" && task.paused) ||
      (statusFilter.value === "active" &&
        !task.paused &&
        !["COMPLETED", "CANCELLED", "STALE"].includes(task.state)) ||
      (statusFilter.value === "closed" &&
        ["COMPLETED", "CANCELLED"].includes(task.state));
    return (
      matchesStatus &&
      (!query ||
        [task.id, task.candidate_id, task.reason, names[task.state], task.state]
          .join(" ")
          .toLocaleLowerCase()
          .includes(query))
    );
  });
});
function clearFilters() {
  search.value = "";
  statusFilter.value = "all";
}
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function refresh() {
  if (disposed || refreshing.value) return;
  refreshing.value = true;
  try {
    const data = await api<{ items: Task[]; runtime_ready: boolean }>(root);
    if (!disposed) {
      tasks.value = data.items.filter((t) => t.branch_id === props.branch.id);
      runtimeReady.value = data.runtime_ready;
    }
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    refreshing.value = false;
  }
}
async function perform(work: () => Promise<void>) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  try {
    await work();
    await refresh();
  } catch (e) {
    fail(e);
  } finally {
    busy.value = false;
  }
}
async function start() {
  await perform(async () => {
    await api(root, "POST", {
      branch_id: props.branch.id,
      candidate_id: candidate.value,
      operation_id: operation.value,
    });
    operation.value = newOperationID();
  });
}
async function control(t: Task, action: string) {
  await perform(async () => {
    await api(`${root}/${t.id}/control`, "POST", {
      action,
      revision: t.revision,
    });
  });
}
onMounted(() => {
  void refresh();
  timer = setInterval(() => {
    if (!busy.value) void refresh();
  }, 2000);
});
onUnmounted(() => {
  disposed = true;
  if (timer) clearInterval(timer);
});
</script>
<template>
  <section class="panel task-browser" aria-label="设定检查任务">
    <div class="section-heading">
      <div>
        <p class="eyebrow">设定候选 / 流程进度</p>
        <h2>持久任务</h2>
      </div>
      <button
        type="button"
        class="quiet"
        :disabled="busy || refreshing"
        @click="refresh"
      >
        刷新持久任务
      </button>
    </div>
    <p class="muted">
      对固定的设定候选进行确定性检查，等待你采用和发布。暂停、重启后仍保留流程；这不替代独立的语义审查，也不会自动付费。
    </p>
    <p v-if="!runtimeReady" class="notice">
      持久任务服务尚未连接，暂不能启动新任务。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p class="muted">启动检查 → 查看进度 → 在原设定流程中采用与发布。</p>
    <form v-if="canEdit" @submit.prevent="start">
      <fieldset :disabled="busy || blocked || !runtimeReady">
        <label
          >待检查候选<select
            v-model="candidate"
            aria-label="待检查候选"
            required
            @change="changed"
          >
            <option value="">请选择尚未发布的候选</option>
            <option v-for="c in available" :key="c.id" :value="c.id">
              {{
                c.snapshot.entities.map((e) => e.name).join("、") || "空设定"
              }}
              · {{ c.id.slice(0, 8) }}
            </option>
          </select></label
        ><button :disabled="!available.some((c) => c.id === candidate)">
          启动持久检查
        </button>
      </fieldset>
    </form>
    <div class="task-toolbar">
      <label
        >查找持久任务<input
          v-model="search"
          type="search"
          placeholder="任务编号、候选编号或原因"
      /></label>
      <label
        >持久任务状态<select v-model="statusFilter" aria-label="持久任务状态">
          <option value="all">全部状态</option>
          <option value="attention">需要处理</option>
          <option value="active">进行中</option>
          <option value="paused">已暂停</option>
          <option value="closed">已结束</option>
        </select></label
      >
      <button type="button" class="quiet" @click="clearFilters">
        清除持久任务筛选
      </button>
    </div>
    <p role="status" class="muted">
      匹配 {{ visibleTasks.length }} / {{ tasks.length }} 项已载入任务 ·
      {{ attentionCount }} 项需要处理
    </p>
    <p v-if="!tasks.length" class="empty">当前分支还没有持久任务。</p>
    <p v-else-if="!visibleTasks.length" class="empty">
      没有符合筛选的持久任务。可清除筛选查看已载入记录。
    </p>
    <article
      v-for="t in visibleTasks"
      :key="t.id"
      class="candidate-card"
      data-testid="task-card"
    >
      <h3>{{ names[t.state] ?? t.state }}</h3>
      <p class="muted">
        任务 {{ t.id.slice(0, 8) }} · 候选 {{ t.candidate_id.slice(0, 8) }}
      </p>
      <p>{{ t.reason }}</p>
      <details>
        <summary>查看固定版本</summary>
        <p>候选 {{ t.candidate_id }} · 内容指纹 {{ t.input_hash }}</p>
      </details>
      <div
        v-if="canEdit && !['COMPLETED', 'CANCELLED', 'STALE'].includes(t.state)"
        class="actions"
      >
        <button
          class="quiet"
          :disabled="busy || blocked"
          @click="control(t, t.paused ? 'resume' : 'pause')"
        >
          {{ t.paused ? "恢复任务" : "暂停任务" }}</button
        ><button
          class="quiet"
          :disabled="busy || blocked"
          @click="control(t, 'refresh')"
        >
          重新检查进度</button
        ><button
          class="quiet"
          :disabled="busy || blocked"
          @click="control(t, 'cancel')"
        >
          取消任务
        </button>
      </div>
    </article>
  </section>
</template>
