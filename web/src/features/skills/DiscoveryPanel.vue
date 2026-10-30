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
interface Source {
  id: string;
  repository: string;
  ref: string;
  path: string;
  keyword: string;
  enabled: boolean;
  limit: number;
  revision: number;
}
interface Run {
  id: string;
  source_id: string;
  state: string;
  reason: string;
  done: number;
  total: number;
  source: Source;
  created_at: string;
  inventory: { commit: string; paths: string[]; total: number } | null;
}
interface Candidate {
  retention?: { state: "EXPIRED"; expired_at: string };
  id: string;
  source_id: string;
  run_id: string;
  path: string;
  commit: string;
  package_id: string;
  previous_id: string;
}
interface Overview {
  sources: Source[];
  runs: Run[];
  candidates: Candidate[];
}
const props = defineProps<{ actor: Actor; project: Project }>();
const emit = defineEmits<{
  expired: [];
  blocking: [value: boolean];
  open: [id: string, previous: string];
  attention: [value: { unknown: number; review: number; active: number }];
}>();
const root = computed(() => `/projects/${props.project.id}/skill-discovery`);
const data = ref<Overview>({ sources: [], runs: [], candidates: [] });
const initial = () => ({
  repository: "anthropics/skills",
  ref: "main",
  path: "skills",
  keyword: "",
  enabled: false,
  limit: 3,
});
const form = ref(initial());
const baseline = ref(JSON.stringify(form.value));
const editing = ref<Source | null>(null);
const operation = ref(newOperationID());
const busy = ref(false);
const error = ref("");
const notice = ref("");
const query = ref("");
const sourceState = ref("");
const taskAttention = computed(() => ({
  unknown: data.value.runs.filter((run) => run.state === "UNKNOWN").length,
  review: 0,
  active: data.value.runs.filter((run) =>
    ["QUEUED", "RUNNING"].includes(run.state),
  ).length,
}));
watch(taskAttention, (value) => emit("attention", value));
const filteredSources = computed(() => {
  const text = query.value.trim().toLocaleLowerCase();
  return data.value.sources.filter(
    (source) =>
      (!sourceState.value ||
        (sourceState.value === "enabled" ? source.enabled : !source.enabled)) &&
      (!text ||
        [source.repository, source.path, source.keyword, source.ref].some(
          (value) => value.toLocaleLowerCase().includes(text),
        )),
  );
});
const dirty = computed(() => JSON.stringify(form.value) !== baseline.value);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const labels: Record<string, string> = {
  QUEUED: "等待后台",
  RUNNING: "正在发现",
  PAUSED: "已暂停",
  CANCELLED: "已取消",
  BLOCKED: "条件变化，已阻断",
  FAILED: "发现失败",
  COMPLETED: "静态研究完成",
};
let disposed = false;
let timer: ReturnType<typeof setTimeout> | undefined;
let request = 0;
watch(
  form,
  () => {
    operation.value = newOperationID();
  },
  { deep: true },
);
watch([dirty, busy], () => emit("blocking", dirty.value || busy.value));
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function refresh() {
  const seq = ++request;
  const project = props.project.id;
  const result = await api<Overview>(root.value);
  if (disposed || seq !== request || project !== props.project.id) return;
  data.value = result;
}
async function poll() {
  try {
    await refresh();
  } catch (e) {
    if (!disposed) fail(e);
  }
  if (!disposed) timer = setTimeout(() => void poll(), 2000);
}
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function reset() {
  form.value = initial();
  editing.value = null;
  baseline.value = JSON.stringify(form.value);
}
function edit(source: Source) {
  if (busy.value || dirty.value) {
    error.value = "请先保存或放弃当前来源输入。";
    return;
  }
  editing.value = { ...source };
  form.value = {
    repository: source.repository,
    ref: source.ref,
    path: source.path,
    keyword: source.keyword,
    enabled: source.enabled,
    limit: source.limit,
  };
  baseline.value = JSON.stringify(form.value);
}
async function save() {
  await perform(async () => {
    const body = {
      ...form.value,
      operation_id: operation.value,
      expected_revision: editing.value?.revision ?? 0,
    };
    await api<Source>(
      `${root.value}/sources${editing.value ? `/${editing.value.id}` : ""}`,
      editing.value ? "PUT" : "POST",
      body,
    );
    if (disposed) return;
    reset();
    await refresh();
    notice.value =
      "来源配置已保存。启用后每日自动发现，也可立即执行；所有结果都是研究候选。";
  });
}
async function toggle(source: Source) {
  await perform(async () => {
    await api<Source>(`${root.value}/sources/${source.id}`, "PUT", {
      repository: source.repository,
      ref: source.ref,
      path: source.path,
      keyword: source.keyword,
      enabled: !source.enabled,
      limit: source.limit,
      expected_revision: source.revision,
      operation_id: newOperationID(),
    });
    await refresh();
  });
}
async function start(source: Source) {
  await perform(async () => {
    await api<Run>(`${root.value}/sources/${source.id}/runs`, "POST", {
      operation_id: newOperationID(),
    });
    await refresh();
  });
}
async function control(run: Run, action: string) {
  await perform(async () => {
    await api<Run>(`${root.value}/runs/${run.id}/control`, "POST", { action });
    await refresh();
  });
}
function active(source: Source) {
  return data.value.runs.some(
    (run) =>
      run.source_id === source.id &&
      ["QUEUED", "RUNNING", "PAUSED"].includes(run.state),
  );
}
onMounted(() => void poll());
onBeforeUnmount(() => {
  disposed = true;
  request++;
  clearTimeout(timer);
});
</script>
<template>
  <section class="panel discovery-panel" aria-labelledby="discovery-title">
    <header class="section-heading">
      <div>
        <p class="eyebrow">登记来源 · 静态分析 · 人工检查</p>
        <h2 id="discovery-title">允许来源内自动发现</h2>
      </div>
    </header>
    <p class="author-page-description">
      只读取登记的公开 GitHub 仓库，每日发现完整 SKILL.md
      目录。任务关键词只在本地筛选，不发送小说或目标内容；不执行包内脚本，不调用付费模型。
    </p>
    <p class="muted">
      每次最多 5 个包，每项目每天最多预留 20
      个包；限额包括失败任务。完整包仍受文件与大小限制。静态分析不代表模型、保留集评测或执行资格通过。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p class="discovery-boundary">
      发现和下载只保留研究候选，不会自动采用、安装或执行。既有来源开关与每次发现仍沿原授权和限额检查。
    </p>
    <p
      v-if="taskAttention.unknown || taskAttention.active"
      class="discovery-boundary"
    >
      已读取的最近任务：结果未知 {{ taskAttention.unknown }} 项 · 进行中
      {{ taskAttention.active }} 项。请在持久任务记录中核对状态。
    </p>
    <details v-if="canEdit" open>
      <summary>{{ editing ? "修改登记来源" : "登记公开来源" }}</summary>
      <p>
        默认示例来自
        <a
          href="https://github.com/anthropics/skills"
          target="_blank"
          rel="noreferrer"
          >Anthropic 官方 Skills 仓库</a
        >；不同目录许可需逐包核对。随系统预置的方法不受网上发现开关影响。
      </p>
      <form @submit.prevent="save">
        <fieldset :disabled="busy">
          <div class="discovery-fields">
            <label
              >来源仓库<input
                v-model="form.repository"
                required
                maxlength="201"
            /></label>
            <label
              >来源分支或提交<input v-model="form.ref" required maxlength="120"
            /></label>
            <label>允许目录<input v-model="form.path" maxlength="240" /></label>
            <label
              >本地目录关键词<input v-model="form.keyword" maxlength="120"
            /></label>
            <label
              >每次候选上限<input
                v-model.number="form.limit"
                type="number"
                min="1"
                max="5"
                required
            /></label>
            <label class="checkbox"
              ><input
                v-model="form.enabled"
                type="checkbox"
              />启用每日发现</label
            >
          </div>
          <button type="submit">
            {{ editing ? "保存来源修改" : "登记来源" }}
          </button>
          <button type="button" class="quiet" @click="reset">
            放弃来源输入
          </button>
        </fieldset>
      </form>
    </details>
    <p v-if="!data.sources.length" class="muted">
      尚未登记来源；已有方法包仍可使用和编辑。
    </p>
    <div class="source-filters">
      <label
        >搜索登记来源<input
          v-model="query"
          type="search"
          aria-label="搜索技能发现来源"
          placeholder="仓库、目录或关键词" /></label
      ><label
        >来源状态<select v-model="sourceState" aria-label="筛选技能来源状态">
          <option value="">全部状态</option>
          <option value="enabled">已启用每日发现</option>
          <option value="disabled">已停用</option>
        </select></label
      >
    </div>
    <p class="muted">
      当前显示 {{ filteredSources.length }} /
      {{ data.sources.length }} 个已读取来源
    </p>
    <article
      v-for="source in filteredSources"
      :key="source.id"
      class="discovery-source"
    >
      <h3>{{ source.repository }} / {{ source.path || "根目录" }}</h3>
      <p>
        {{ source.ref }} · {{ source.enabled ? "每日发现已启用" : "已停用" }} ·
        配置版本 {{ source.revision }} · 每次 {{ source.limit }} 包
      </p>
      <p v-if="source.keyword">本地筛选：{{ source.keyword }}</p>
      <div v-if="canEdit" class="actions">
        <button
          :disabled="busy || !source.enabled || active(source)"
          @click="start(source)"
        >
          立即发现
        </button>
        <button class="quiet" :disabled="busy" @click="toggle(source)">
          {{ source.enabled ? "停用来源" : "启用来源" }}
        </button>
        <button class="quiet" :disabled="busy || dirty" @click="edit(source)">
          编辑来源
        </button>
      </div>
    </article>
    <details :open="data.runs.length > 0">
      <summary>持久任务与进度（最近 30 次）</summary>
      <article v-for="run in data.runs" :key="run.id" class="discovery-run">
        <strong
          >{{ labels[run.state] || run.state }} · {{ run.done }}/{{
            run.total
          }}
          个候选</strong
        >
        <p>
          {{ run.source.repository }} / {{ run.source.path }} ·
          {{ run.created_at }} UTC
        </p>
        <p v-if="run.inventory">
          本地匹配 {{ run.inventory.total }} 个目录；固定提交
          {{ run.inventory.commit }}
        </p>
        <p>{{ run.reason }}</p>
        <div
          v-if="canEdit && ['QUEUED', 'RUNNING', 'PAUSED'].includes(run.state)"
          class="actions"
        >
          <button
            class="quiet"
            :disabled="busy"
            @click="control(run, run.state === 'PAUSED' ? 'resume' : 'pause')"
          >
            {{ run.state === "PAUSED" ? "恢复发现" : "暂停发现" }}
          </button>
          <button
            class="quiet"
            :disabled="busy"
            @click="control(run, 'cancel')"
          >
            取消发现
          </button>
        </div>
      </article>
    </details>
    <details :open="data.candidates.length > 0">
      <summary>新候选与上游变化（最近 100 个）</summary>
      <article
        v-for="candidate in data.candidates"
        :key="candidate.id"
        class="discovery-candidate"
      >
        <strong>{{
          candidate.retention ? "来源路径已过期" : candidate.path
        }}</strong>
        <p>来源提交 {{ candidate.commit }}</p>
        <p>
          {{
            candidate.previous_id
              ? "发现新来源版本；本地编辑保留，需人工比较与合并。"
              : "新发现的研究候选；尚未取得执行资格。"
          }}
        </p>
        <p v-if="candidate.previous_id">
          旧来源包 {{ candidate.previous_id }} → 新来源包
          {{ candidate.package_id }}
        </p>
        <button
          class="quiet"
          :disabled="busy || dirty || !!candidate.retention"
          @click="emit('open', candidate.package_id, candidate.previous_id)"
        >
          查看静态发现、完整文件并编辑
        </button>
      </article>
    </details>
  </section>
</template>
<style scoped>
.discovery-panel {
  margin-block: 1rem;
}
.source-filters {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  padding-top: 20px;
  border-top: 1px solid #e1e5da;
}
.discovery-boundary {
  padding: 14px 16px;
  background: #f5f6ef;
  border-left: 3px solid #91ad98;
  font-size: 13px;
}
@media (max-width: 680px) {
  .source-filters {
    grid-template-columns: minmax(0, 1fr);
  }
}
.discovery-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 0.8rem;
}
fieldset {
  border: 0;
  padding: 0;
}
fieldset button {
  margin-top: 0.8rem;
  margin-right: 0.5rem;
}
.discovery-source,
.discovery-run,
.discovery-candidate {
  border-top: 1px solid var(--border, #ddd);
  padding: 1rem 0;
  overflow-wrap: anywhere;
}
.checkbox {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.checkbox input {
  width: auto;
}
.actions {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}
summary {
  cursor: pointer;
  margin-block: 0.8rem;
}
</style>
