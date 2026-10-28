<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import type { Branch } from "./types";
interface Job {
  kind: "published" | "working";
  id: string;
  release_id: string;
  candidate_id: string;
  index_id: string;
  state: string;
  reason: string;
  done_sources: number;
  total_sources: number;
  chunks: number;
  stale: boolean;
  can_cancel: boolean;
}
const props = withDefaults(
  defineProps<{
    project: Project;
    actor: Actor;
    branch: Branch;
    blocked: boolean;
    releaseId?: string;
    candidateId?: string;
    revision?: number;
    label?: string;
    kind?: "published" | "working";
  }>(),
  {
    releaseId: "",
    candidateId: "",
    revision: 0,
    label: "构建 / 更新检索索引",
    kind: "published",
  },
);
const emit = defineEmits<{
  expired: [];
  queued: [];
  ready: [{ chunks: number; index_id: string }];
}>();
const root = `/projects/${props.project.id}/branches/${props.branch.id}/knowledge/jobs`;
const jobs = ref<Job[]>([]);
const busy = ref(false);
const error = ref("");
const runtimeReady = ref(true);
const operation = ref(newOperationID());
const prospective = computed(() =>
  Boolean(props.releaseId || props.candidateId),
);
const canStart = computed(() => {
  const allowed = prospective.value
    ? ["owner", "publisher"]
    : ["owner", "editor"];
  return (
    allowed.includes(props.actor.role) && allowed.includes(props.project.role)
  );
});
const names: Record<string, string> = {
  QUEUED: "等待后台处理",
  RUNNING: "正在构建索引",
  READY: "索引已就绪",
  CANCELLED: "已停止",
  BLOCKED: "需要按新版本重建",
  FAILED: "构建未完成",
};
let disposed = false;
let timer: ReturnType<typeof setTimeout> | undefined;
let sequence = 0;
const notified = new Set<string>();
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function refresh() {
  const current = ++sequence;
  try {
    const result = await api<{ items: Job[]; runtime_ready: boolean }>(root);
    if (disposed || current !== sequence) return;
    runtimeReady.value = result.runtime_ready;
    jobs.value = result.items.filter(
      (j) =>
        (j.kind ?? "published") === props.kind &&
        j.release_id === props.releaseId &&
        j.candidate_id === props.candidateId,
    );
    const ready = jobs.value.find((j) => j.state === "READY" && !j.stale);
    if (ready && !notified.has(ready.id)) {
      notified.add(ready.id);
      emit("ready", { chunks: ready.chunks, index_id: ready.index_id });
    }
  } catch (e) {
    if (!disposed && current === sequence) fail(e);
  } finally {
    if (!disposed && current === sequence) {
      clearTimeout(timer);
      if (jobs.value.some((j) => ["QUEUED", "RUNNING"].includes(j.state)))
        timer = setTimeout(() => void refresh(), 1500);
    }
  }
}
async function start() {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  try {
    await api<Job>(root, "POST", {
      operation_id: operation.value,
      release_id: props.releaseId,
      candidate_id: props.candidateId,
      kind: props.kind,
    });
    if (disposed) return;
    operation.value = newOperationID();
    emit("queued");
    await refresh();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    busy.value = false;
  }
}
async function cancel(job: Job) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await api(`${root}/${job.id}/cancel`, "POST", {});
    await refresh();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    busy.value = false;
  }
}
watch(
  () => [props.branch.head, props.revision],
  () => void refresh(),
);
onMounted(() => void refresh());
onBeforeUnmount(() => {
  disposed = true;
  sequence++;
  clearTimeout(timer);
});
</script>
<template>
  <section
    class="index-jobs"
    :data-testid="
      kind === 'working'
        ? 'private-index-jobs'
        : prospective
          ? 'prospective-index-jobs'
          : 'index-jobs'
    "
  >
    <button
      type="button"
      v-if="canStart"
      class="secondary"
      :disabled="busy || blocked"
      @click="start"
    >
      {{ label }}
    </button>
    <p class="muted">
      在本机后台处理，可关闭此页面后回来查看；预建索引不代表采用或发布内容。
    </p>
    <p v-if="!runtimeReady" class="notice">
      后台任务服务暂未连接；已保存的任务会等待服务恢复。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <article v-for="job in jobs.slice(0, 8)" :key="job.id" class="index-job">
      <strong>{{ names[job.state] ?? job.state }}</strong>
      <p v-if="job.state === 'READY' && !job.stale">
        {{ job.chunks }} 个原文片段已校验。
      </p>
      <p v-else>
        已处理 {{ job.done_sources }} / {{ job.total_sources }} 份资料 ·
        {{ job.chunks }} 个原文片段
      </p>
      <p v-if="job.stale" class="notice">
        输入版本已变化或无法核对；这项历史任务不能代表当前索引。
      </p>
      <p v-if="job.reason">{{ job.reason }}</p>
      <button
        type="button"
        v-if="job.can_cancel"
        class="quiet"
        :disabled="busy"
        @click="cancel(job)"
      >
        停止此索引任务
      </button>
    </article>
  </section>
</template>
<style scoped>
.index-jobs {
  margin: 1rem 0;
}
.index-job {
  padding: 0.8rem;
  margin: 0.6rem 0;
  border: 1px solid #d7d6ce;
  border-radius: 10px;
}
</style>
