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
  <section class="panel">
    <h2>持久任务</h2>
    <p class="muted">
      对固定的设定候选进行确定性检查，等待你采用和发布。暂停、重启后仍保留流程；这不替代独立的语义审查，也不会自动付费。
    </p>
    <p v-if="!runtimeReady" class="notice">
      持久任务服务尚未连接，暂不能启动新任务。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
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
    <p v-if="!tasks.length" class="empty">当前分支还没有持久任务。</p>
    <article
      v-for="t in tasks"
      :key="t.id"
      class="candidate-card"
      data-testid="task-card"
    >
      <h3>{{ names[t.state] ?? t.state }}</h3>
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
          :disabled="busy || blocked"
          @click="control(t, t.paused ? 'resume' : 'pause')"
        >
          {{ t.paused ? "恢复任务" : "暂停任务" }}</button
        ><button :disabled="busy || blocked" @click="control(t, 'refresh')">
          重新检查进度</button
        ><button :disabled="busy || blocked" @click="control(t, 'cancel')">
          取消任务
        </button>
      </div>
    </article>
  </section>
</template>
