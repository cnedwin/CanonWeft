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
const search = ref("");
const statusFilter = ref("all");
const visibleCount = ref(8);
const filteredJobs = computed(() => {
  const query = search.value.trim().toLocaleLowerCase();
  return jobs.value.filter((job) => {
    const matchesStatus =
      statusFilter.value === "all" ||
      (statusFilter.value === "attention" &&
        (job.stale || ["FAILED", "BLOCKED"].includes(job.state))) ||
      (statusFilter.value === "active" &&
        ["QUEUED", "RUNNING"].includes(job.state)) ||
      (statusFilter.value === "ready" && job.state === "READY" && !job.stale) ||
      (statusFilter.value === "cancelled" && job.state === "CANCELLED");
    return (
      matchesStatus &&
      (!query ||
        [job.id, job.index_id, job.reason, names[job.state], job.state]
          .join(" ")
          .toLocaleLowerCase()
          .includes(query))
    );
  });
});
watch([search, statusFilter], () => {
  visibleCount.value = 8;
});
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
    class="index-jobs task-browser"
    :data-testid="
      kind === 'working'
        ? 'private-index-jobs'
        : prospective
          ? 'prospective-index-jobs'
          : 'index-jobs'
    "
  >
    <div class="section-heading">
      <h3>检索索引任务</h3>
      <button type="button" class="quiet" :disabled="busy" @click="refresh">
        刷新索引任务
      </button>
    </div>
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
    <div class="task-toolbar">
      <label
        >查找索引任务<input
          v-model="search"
          type="search"
          placeholder="任务编号、索引编号或原因"
      /></label>
      <label
        >索引任务状态<select v-model="statusFilter" aria-label="索引任务状态">
          <option value="all">全部状态</option>
          <option value="attention">需要处理</option>
          <option value="active">进行中</option>
          <option value="ready">已就绪</option>
          <option value="cancelled">已停止</option>
        </select></label
      >
      <button
        type="button"
        class="quiet"
        @click="
          search = '';
          statusFilter = 'all';
        "
      >
        清除索引任务筛选
      </button>
    </div>
    <p class="muted" role="status">
      匹配 {{ filteredJobs.length }} / {{ jobs.length }} 项已载入任务
    </p>
    <p v-if="!jobs.length" class="empty">当前范围尚无检索索引任务。</p>
    <p v-else-if="!filteredJobs.length" class="empty">
      没有符合筛选的索引任务，可清除筛选查看已载入记录。
    </p>
    <article
      v-for="job in filteredJobs.slice(0, visibleCount)"
      :key="job.id"
      class="index-job"
    >
      <strong>{{ names[job.state] ?? job.state }}</strong>
      <p class="muted">任务 {{ job.id.slice(0, 8) }}</p>
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
    <button
      v-if="filteredJobs.length > visibleCount"
      type="button"
      class="quiet"
      @click="visibleCount += 8"
    >
      展开更多已载入索引任务
    </button>
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
