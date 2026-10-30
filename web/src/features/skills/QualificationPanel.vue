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
import { capabilityKinds, type CapabilityVersion } from "./capabilityTypes";
import ExpiredTaskHistory from "../../components/ExpiredTaskHistory.vue";
import NovelSemanticResults from "./NovelSemanticResults.vue";
import type { NovelSemanticSummary } from "./novelSemanticTypes";

const props = defineProps<{
  actor: Actor;
  project: Project;
  initialSuite?: string;
}>();
const emit = defineEmits<{
  expired: [];
  blocking: [boolean];
  attention: [value: { unknown: number; review: number; active: number }];
}>();
type Suite = {
  id: string;
  name: string;
  hash: string;
  task_kind: string;
  scope: string;
  description: string;
  repeats: number;
  cases: {
    id: string;
    description: string;
    input: unknown;
    negative: boolean;
  }[];
};
type Price = {
  id: string;
  currency: string;
  connection_id: string;
  connection_revision: number;
};
type Form = {
  operation_id: string;
  version_id: string;
  baseline_id: string;
  branch_id: string;
  expected_ip_head: string;
  expected_binding: string;
  suite_id: string;
  role: string;
  price_id: string;
  max_output_tokens: number;
};
type Frozen = {
  input: Form;
  suite: Suite;
  candidate: { id: string; hash: string; kind: string };
  candidate_manifest: string;
  config: { id: string; revision: number; model: string };
  runtime_hash: string;
  estimate_micros: number;
  price: Price;
};
type Quote = {
  ready: boolean;
  reasons: string[];
  frozen: Frozen;
  calls: number;
};
type Step = {
  id: string;
  arm: string;
  case_id: string;
  repeat: number;
  state: string;
  child_id: string;
  metrics: {
    output: string;
    output_hash: string;
    cost_micros: number;
    duration_ms: number;
    usage_known: boolean;
    cost_known: boolean;
    accounting_revision: number;
    receipt_count: number;
    manual_repairs: number;
    score: {
      passed: boolean;
      checks: { code: string; passed: boolean; detail: string }[];
    };
  };
};
type Run = {
  novel_semantics?: NovelSemanticSummary;
  retention?: { state: "EXPIRED"; expired_at: string };
  id: string;
  state: string;
  reason: string;
  stale: boolean;
  frozen: Frozen;
  steps: Step[];
  report_hash: string;
  qualification_id: string;
};
type Binding = {
  id: string;
  task_kind: string;
  qualification_id: string;
  previous_id: string;
  enabled: boolean;
  current: boolean;
  valid: boolean;
  qualification?: { expires_at: string; frozen: Frozen };
};
const root = `/projects/${props.project.id}/qualification`;
const suites = ref<Suite[]>([]);
const versions = ref<CapabilityVersion[]>([]);
const branches = ref<{ id: string; name: string; head: string }[]>([]);
const options = ref<
  { role: string; name: string; model: string; price: Price }[]
>([]);
const runs = ref<Run[]>([]);
const bindings = ref<Binding[]>([]);
const selected = ref<Run | null>(null);
const runQuery = ref("");
const runState = ref("");
const knownRuns = computed(() => {
  const known = new Map(
    runs.value.filter((run) => !run.retention).map((run) => [run.id, run]),
  );
  if (selected.value && !selected.value.retention)
    known.set(selected.value.id, selected.value);
  return [...known.values()];
});
const taskAttention = computed(() => ({
  unknown: knownRuns.value.filter(
    (run) =>
      run.state === "UNKNOWN" ||
      (run.steps ?? []).some((step) => step.state === "UNKNOWN"),
  ).length,
  review: knownRuns.value.filter((run) => run.state === "REVIEW_REQUIRED")
    .length,
  active: knownRuns.value.filter((run) =>
    ["QUEUED", "RUNNING"].includes(run.state),
  ).length,
}));
watch(taskAttention, (value) => emit("attention", value));
const filteredRuns = computed(() => {
  const text = runQuery.value.trim().toLocaleLowerCase();
  return runs.value.filter(
    (run) =>
      !run.retention &&
      (!runState.value || run.state === runState.value) &&
      (!text ||
        [run.id, run.frozen.suite.name, labels[run.state] ?? run.state].some(
          (value) => value.toLocaleLowerCase().includes(text),
        )),
  );
});
const visibleStepCount = ref(50);
watch(
  () => selected.value?.id,
  () => {
    visibleStepCount.value = 50;
  },
);
const quote = ref<Quote | null>(null);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const note = ref("");
const reason = ref("");
const weeklyEnabled = ref(false);
const weeklyLimit = ref(0);
const weeklyScope = ref<Form | null>(null);
const weeklyOriginal = ref("");
const scheduleRevision = ref(0);
const scheduleReason = ref("");
const form = ref<Form>({
  operation_id: newOperationID(),
  version_id: "",
  baseline_id: "",
  branch_id: "",
  expected_ip_head: "",
  expected_binding: "",
  suite_id: props.initialSuite ?? "shared-contract-v1",
  role: [
    "shared-semantic-review-v1",
    "shared-change-impact-v1",
    "novel-semantic-acceptance-v1",
  ].includes(props.initialSuite || "")
    ? "critic"
    : "researcher",
  price_id: "",
  max_output_tokens: [
    "shared-semantic-review-v1",
    "shared-change-impact-v1",
    "novel-semantic-acceptance-v1",
  ].includes(props.initialSuite || "")
    ? 1024
    : 512,
});
const original = ref("");
function weeklySnapshot() {
  return JSON.stringify({
    input: weeklyScope.value
      ? { ...weeklyScope.value, operation_id: "" }
      : null,
    enabled: weeklyEnabled.value,
    limit: weeklyLimit.value,
  });
}
const weeklyDirty = computed(
  () =>
    weeklyOriginal.value !== "" && weeklySnapshot() !== weeklyOriginal.value,
);
const dirty = computed(
  () =>
    (original.value !== "" &&
      JSON.stringify({ ...form.value, operation_id: "" }) !==
        JSON.stringify({
          ...(JSON.parse(original.value) as Form),
          operation_id: "",
        })) ||
    weeklyDirty.value ||
    !!note.value ||
    !!reason.value,
);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const owner = computed(
  () => props.actor.role === "owner" && props.project.role === "owner",
);
const candidate = computed(() =>
  versions.value.find((v) => v.id === form.value.version_id),
);
const suite = computed(() =>
  suites.value.find((v) => v.id === form.value.suite_id),
);
const labels: Record<string, string> = {
  NOT_RUN: "未执行",
  QUEUED: "等待评测",
  RUNNING: "正在评测",
  PAUSED: "已暂停",
  BLOCKED: "条件不满足",
  CANCELLED: "已取消",
  FAILED: "评测未通过",
  FAILED_CHECK: "检查未通过",
  SUCCEEDED: "执行完成",
  PASSED: "机械用途资格通过",
  REVIEW_REQUIRED: "等待维护角色检查执行依赖",
  UNKNOWN: "回执未知",
  REJECTED: "供应商拒绝",
  INVALID_OUTPUT: "输出不符合契约",
};
const armLabels: Record<string, string> = {
  no_method: "无方法",
  baseline: "现有基线",
  candidate: "候选版本",
};
let disposed = false;
let sequence = 0;
let polling = false;
let timer: ReturnType<typeof setInterval> | undefined;
watch(dirty, (value) => emit("blocking", value), { immediate: true });
watch(
  () => form.value.suite_id,
  (id) => {
    if (
      [
        "shared-semantic-review-v1",
        "shared-change-impact-v1",
        "novel-semantic-acceptance-v1",
      ].includes(id)
    )
      form.value.role = "critic";
  },
);
let formSnapshot = "";
watch(
  form,
  () => {
    quote.value = null;
    const next = JSON.stringify({ ...form.value, operation_id: "" });
    if (next !== formSnapshot) {
      formSnapshot = next;
      form.value.operation_id = newOperationID();
    }
  },
  { deep: true, flush: "sync" },
);
watch(
  () => form.value.role,
  (role) => {
    form.value.price_id =
      options.value.find((v) => v.role === role)?.price.id ?? "";
  },
);
function fail(e: unknown) {
  if (disposed) return;
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    fail(e);
  } finally {
    busy.value = false;
  }
}
async function refresh() {
  const [history, active, capabilities] = await Promise.all([
    api<{ items: Run[] }>(`${root}/runs`),
    api<{ items: Binding[] }>(`${root}/bindings`),
    api<{ items: CapabilityVersion[] }>(
      `/projects/${props.project.id}/capabilities`,
    ),
  ]);
  if (disposed) return;
  runs.value = history.items;
  if (
    history.items.some((run) => run.id === selected.value?.id && run.retention)
  ) {
    ++sequence;
    selected.value = null;
  }
  bindings.value = active.items;
  versions.value = capabilities.items;
}
async function read(id: string) {
  const ticket = ++sequence;
  try {
    const result = await api<Run>(`${root}/runs/${id}`);
    if (!disposed && ticket === sequence) selected.value = result;
  } catch (e) {
    if (
      !disposed &&
      ticket === sequence &&
      e instanceof APIError &&
      e.status === 410
    )
      selected.value = null;
    throw e;
  }
}
function branchChanged() {
  form.value.expected_ip_head =
    branches.value.find((b) => b.id === form.value.branch_id)?.head ?? "";
}
function purposeChanged() {
  const current = bindings.value.find(
    (b) => b.current && b.task_kind === suite.value?.task_kind,
  );
  form.value.expected_binding = current?.id ?? "";
  form.value.baseline_id = current?.qualification?.frozen.candidate.id ?? "";
}
async function prepareBaselines() {
  await perform(async () => {
    if (!branches.value.length) {
      const branch = await api<{ id: string; name: string; head: string }>(
        `/projects/${props.project.id}/branches`,
        "POST",
        { name: "评测工作线", operation_id: newOperationID() },
      );
      if (disposed) return;
      branches.value = [branch];
      form.value.branch_id = branch.id;
      branchChanged();
    }
    const result = await api<{ baseline_id: string }>(
      `${root}/baseline-presets`,
      "POST",
      {
        suite_id: form.value.suite_id,
        kind: candidate.value?.definition.kind ?? "agent",
        role: form.value.role,
      },
    );
    if (disposed) return;
    form.value.baseline_id = result.baseline_id;
    if (!form.value.version_id) form.value.version_id = result.baseline_id;
    await refresh();
    notice.value = "已准备固定的可执行对照。准备本身不会授予资格或调用模型。";
  });
}
async function estimate() {
  await perform(async () => {
    const [branchList, active] = await Promise.all([
      api<{ items: typeof branches.value }>(
        `/projects/${props.project.id}/branches`,
      ),
      api<{ items: Binding[] }>(`${root}/bindings`),
    ]);
    if (disposed) return;
    branches.value = branchList.items;
    bindings.value = active.items;
    form.value.expected_binding =
      bindings.value.find(
        (b) => b.current && b.task_kind === suite.value?.task_kind,
      )?.id ?? "";
    branchChanged();
    const input = { ...form.value };
    const result = await api<Quote>(`${root}/quote`, "POST", input);
    if (!disposed && JSON.stringify(input) === JSON.stringify(form.value))
      quote.value = result;
  });
}
async function start() {
  if (!quote.value?.ready) return;
  await perform(async () => {
    const input = { ...form.value };
    const result = await api<Run>(`${root}/runs`, "POST", input);
    if (disposed) return;
    selected.value = result;
    sequence++;
    original.value = JSON.stringify(form.value);
    await refresh();
  });
}
async function control(action: string) {
  if (!selected.value) return;
  const id = selected.value.id;
  await perform(async () => {
    await api(`${root}/runs/${id}/control`, "POST", { action });
    await read(id);
    await refresh();
  });
}
async function approve() {
  if (!selected.value) return;
  const run = selected.value;
  await perform(async () => {
    await api(`${root}/runs/${run.id}/approve-executable`, "POST", {
      report_hash: run.report_hash,
      note: note.value,
    });
    if (disposed) return;
    note.value = "";
    await read(run.id);
    await refresh();
  });
}
async function activate(run: Run) {
  const current = bindings.value.find(
    (b) => b.current && b.task_kind === run.frozen.suite.task_kind,
  );
  await perform(async () => {
    await api(`${root}/bindings`, "POST", {
      operation_id: newOperationID(),
      qualification_id: run.qualification_id,
      expected_binding: current?.id ?? "",
      task_kind: run.frozen.suite.task_kind,
    });
    await refresh();
    await read(run.id);
    notice.value =
      "已启用精确用途绑定。后续任务使用该版本，正在运行的任务仍固定原绑定。";
  });
}
async function rollback(binding: Binding) {
  const current = bindings.value.find(
    (b) => b.current && b.task_kind === binding.task_kind,
  );
  await perform(async () => {
    await api(`${root}/bindings`, "POST", {
      operation_id: newOperationID(),
      qualification_id: binding.qualification_id,
      expected_binding: current?.id ?? "",
      task_kind: binding.task_kind,
      rollback: true,
    });
    await refresh();
    notice.value = "已回退到仍有效的用途资格；后续任务采用该绑定。";
  });
}
async function disable(binding: Binding) {
  await perform(async () => {
    await api(`${root}/bindings/${binding.id}/disable`, "POST", {});
    await refresh();
    notice.value =
      "已停用该绑定；若它是当前选择，已清除用途头。使用该绑定的任务将在下一检查点停止，已派发未知费用仍保留。";
  });
}
async function revoke() {
  if (!selected.value?.qualification_id) return;
  const run = selected.value;
  await perform(async () => {
    await api(
      `${root}/records/${run.qualification_id}/revoke-version`,
      "POST",
      { reason: reason.value },
    );
    if (disposed) return;
    reason.value = "";
    await read(run.id);
    await refresh();
    notice.value =
      "已撤销整个能力版本，包含依赖它的资格与派生用途；历史证据保留。";
  });
}
async function saveWeekly() {
  await perform(async () => {
    const result = await api<{
      revision: number;
      reason: string;
      input: Form;
      enabled: boolean;
      budget_limit_micros: number;
    }>(`${root}/schedule`, "PUT", {
      input: weeklyScope.value
        ? { ...weeklyScope.value, operation_id: "" }
        : undefined,
      enabled: weeklyEnabled.value,
      budget_limit_micros: Math.round(weeklyLimit.value * 1000000),
      revision: scheduleRevision.value,
    });
    if (disposed) return;
    scheduleRevision.value = result.revision;
    scheduleReason.value = result.reason;
    weeklyScope.value = result.input?.version_id
      ? { ...result.input, operation_id: "" }
      : null;
    weeklyEnabled.value = result.enabled;
    weeklyLimit.value = result.budget_limit_micros / 1000000;
    weeklyOriginal.value = weeklySnapshot();
    notice.value =
      "已保存项目每周评测设置。费用上限默认 0；当前权限、基线、分支和预算不满足时会停止并记录待处理原因。";
  });
}
function copyWeeklyScope() {
  if (!form.value.version_id || !form.value.branch_id) {
    error.value =
      "先在上方选择要固定的候选版本和故事分支，再明确替换周评测范围。";
    return;
  }
  weeklyScope.value = { ...form.value, operation_id: "" };
  notice.value =
    "已把上方范围复制为待保存的周评测范围。保存后才替换原计划；仅切换上方表单不会修改周计划。";
}
function discardWeekly() {
  if (!weeklyOriginal.value) return;
  const saved = JSON.parse(weeklyOriginal.value) as {
    input: Form | null;
    enabled: boolean;
    limit: number;
  };
  weeklyScope.value = saved.input;
  weeklyEnabled.value = saved.enabled;
  weeklyLimit.value = saved.limit;
}
function summary(arm: string) {
  const steps = selected.value?.steps?.filter((s) => s.arm === arm) ?? [];
  return {
    count: steps.length,
    passed: steps.filter((s) => s.metrics.score?.passed).length,
    cost: steps.reduce((n, s) => n + s.metrics.cost_micros, 0),
    duration: steps.reduce((n, s) => n + s.metrics.duration_ms, 0),
  };
}
function discard() {
  note.value = "";
  reason.value = "";
  if (original.value) form.value = JSON.parse(original.value) as Form;
  discardWeekly();
}
onMounted(() => {
  void perform(async () => {
    const [catalog, branchList, modelOptions, schedule] = await Promise.all([
      api<{ items: Suite[] }>(`${root}/suites`),
      api<{ items: typeof branches.value }>(
        `/projects/${props.project.id}/branches`,
      ),
      api<{ items: typeof options.value }>(
        `/projects/${props.project.id}/generation-options`,
      ),
      api<{
        input: Form;
        enabled: boolean;
        budget_limit_micros: number;
        revision: number;
        reason: string;
      }>(`${root}/schedule`),
    ]);
    if (disposed) return;
    weeklyEnabled.value = schedule.enabled ?? false;
    weeklyLimit.value = (schedule.budget_limit_micros ?? 0) / 1000000;
    scheduleRevision.value = schedule.revision ?? 0;
    scheduleReason.value = schedule.reason ?? "";
    weeklyScope.value = schedule.input?.version_id
      ? { ...schedule.input, operation_id: "" }
      : null;
    weeklyOriginal.value = weeklySnapshot();
    suites.value = catalog.items;
    branches.value = branchList.items;
    options.value = modelOptions.items;
    form.value.branch_id = branchList.items[0]?.id ?? "";
    branchChanged();
    form.value.price_id =
      modelOptions.items.find((o) => o.role === form.value.role)?.price.id ??
      "";
    await refresh();
    purposeChanged();
    original.value = JSON.stringify(form.value);
  });
  timer = setInterval(() => {
    if (disposed || busy.value || polling || !selected.value) return;
    polling = true;
    void read(selected.value.id)
      .catch(fail)
      .finally(() => {
        polling = false;
      });
  }, 2000);
});
onBeforeUnmount(() => {
  disposed = true;
  sequence++;
  if (timer) clearInterval(timer);
  emit("blocking", false);
});
</script>

<template>
  <section class="panel qualification-panel" aria-label="独立评测与用途启用">
    <header class="section-heading">
      <div>
        <p class="eyebrow">准备范围 · 独立证据 · 明确启用</p>
        <h2>独立评测与用途启用</h2>
      </div>
    </header>
    <p class="author-page-description">
      固定能力清单、模型或隔离运行环境及任务范围。同一任务比较无方法、现有基线和候选版本，各自使用新上下文并重复运行。通过机械检查仍未取得小说文学、视觉或游戏专业质量资格。
    </p>
    <p>
      三组对照共用同一冻结模型与精确输出档位（工具共用固定镜像）。要更换当前绑定的配置，请先明确停用当前绑定，再选初始基线和新配置完成全部评测。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <aside
      v-if="
        taskAttention.unknown || taskAttention.review || taskAttention.active
      "
      class="qualification-attention"
    >
      已读取评测：结果未知 {{ taskAttention.unknown }} 项 · 等待维护检查
      {{ taskAttention.review }} 项 · 进行中
      {{ taskAttention.active }}
      项。请在下方历史中核对；未知回执与费用不会因页面切换而自动重试或重新授予资格。
    </aside>
    <h3>1. 准备固定的评测范围</h3>
    <form @submit.prevent="estimate">
      <fieldset :disabled="busy || !canEdit">
        <label
          >可信评测集<select v-model="form.suite_id" @change="purposeChanged">
            <option v-for="s in suites" :key="s.id" :value="s.id">
              {{ s.name }} · 版本 {{ s.id }}
            </option>
          </select></label
        >
        <p>{{ suite?.description }}</p>
        <label
          >固定候选版本<select v-model="form.version_id" required>
            <option value="" disabled>选择 Agent、Skill 或 Tool</option>
            <option
              v-for="v in versions.filter(
                (v) => v.definition.kind !== 'recipe',
              )"
              :key="v.id"
              :value="v.id"
            >
              {{ v.definition.name }} ·
              {{ capabilityKinds[v.definition.kind] }} · {{ v.id.slice(0, 8) }}
            </option>
          </select></label
        >
        <label
          >现有基线版本<select v-model="form.baseline_id" required>
            <option value="" disabled>选择当前或初始可信基线</option>
            <option
              v-for="v in versions.filter(
                (v) =>
                  !candidate || v.definition.kind === candidate.definition.kind,
              )"
              :key="v.id"
              :value="v.id"
            >
              {{ v.definition.name }} · {{ v.id.slice(0, 8) }}
            </option>
          </select></label
        >
        <button type="button" class="quiet" @click="prepareBaselines">
          准备可信初始对照
        </button>
        <label
          >固定故事分支<select
            v-model="form.branch_id"
            required
            @change="branchChanged"
          >
            <option v-for="b in branches" :key="b.id" :value="b.id">
              {{ b.name }}
            </option>
          </select></label
        >
        <template v-if="candidate?.definition.kind !== 'tool'">
          <label
            >模型用途与配置<select v-model="form.role" required>
              <option v-for="o in options" :key="o.role" :value="o.role">
                {{ o.role }} · {{ o.name }} · {{ o.model }} · 配置版本
                {{ o.price.connection_revision }}
              </option>
            </select></label
          >
          <p v-if="!options.length">
            尚未配置可用的模型用途和有效价格；不会发送请求。
          </p>
          <label
            >精确输出上限（资格只适用于此参数）<input
              v-model.number="form.max_output_tokens"
              type="number"
              min="1"
              max="8192"
              required
          /></label>
        </template>
        <div class="actions">
          <button>检查条件与费用估算</button
          ><button
            type="button"
            class="quiet"
            :disabled="!dirty"
            @click="discard"
          >
            放弃评测表单修改
          </button>
        </div>
      </fieldset>
    </form>
    <article v-if="quote" class="notice">
      <p>
        共 {{ quote.calls }} 次独立执行；费用预估
        {{ quote.frozen.estimate_micros / 1000000 }}
        {{
          quote.frozen.price.currency || "（本地免费工具）"
        }}。执行时仍检查当前授权和预算。
      </p>
      <ul>
        <li v-for="item in quote.reasons" :key="item">{{ item }}</li>
      </ul>
      <details>
        <summary>固定清单、配置和评测集哈希</summary>
        <pre>{{ quote.frozen }}</pre>
      </details>
      <button :disabled="busy || !canEdit || !quote.ready" @click="start">
        开始独立对照评测
      </button>
    </article>
    <div class="section-heading qualification-history-heading">
      <h3>2. 评测记录与证据</h3>
      <button class="quiet" :disabled="busy" @click="perform(refresh)">
        刷新评测与启用历史
      </button>
    </div>
    <div class="qualification-filters">
      <label
        >搜索评测记录<input
          v-model="runQuery"
          type="search"
          aria-label="搜索独立评测记录"
          placeholder="评测集、状态或记录编号" /></label
      ><label
        >评测状态<select v-model="runState" aria-label="筛选独立评测状态">
          <option value="">全部状态</option>
          <option v-for="(name, value) in labels" :key="value" :value="value">
            {{ name }}
          </option>
        </select></label
      >
    </div>
    <p class="muted">
      当前显示 {{ filteredRuns.length }} /
      {{ runs.filter((run) => !run.retention).length }}
      个已读取评测。资格仅对应所列固定版本与范围。
    </p>
    <ul class="qualification-runs">
      <li v-for="run in filteredRuns" :key="run.id">
        <button
          class="quiet"
          :aria-pressed="selected?.id === run.id"
          :disabled="busy"
          @click="perform(() => read(run.id))"
        >
          {{ labels[run.state] || run.state }} · {{ run.frozen.suite.name }} ·
          {{ run.id.slice(0, 8) }}
        </button>
      </li>
    </ul>
    <ExpiredTaskHistory scope="当前项目评测" :items="runs" />
    <article v-if="selected">
      <h3>
        {{
          selected.state === "PASSED" &&
          [
            "shared-semantic-review-v1",
            "shared-change-impact-v1",
            "novel-semantic-acceptance-v1",
          ].includes(selected.frozen.suite.id)
            ? "所列独立审查用途资格通过"
            : labels[selected.state] || selected.state
        }}
      </h3>
      <p>{{ selected.reason }}</p>
      <NovelSemanticResults
        v-if="selected.novel_semantics"
        :summary="selected.novel_semantics"
      />
      <p>
        当前回执与账单会继续更新；已保存的完整证据报告保持原样。晚到账单不会重新授予资格。
      </p>
      <p v-if="selected.stale" role="alert">
        冻结的条件已有变化；本次记录不能直接用于新启用，历史证据仍保留。
      </p>
      <div
        v-if="
          canEdit && ['QUEUED', 'RUNNING', 'PAUSED'].includes(selected.state)
        "
        class="actions"
      >
        <button
          :disabled="busy"
          @click="control(selected.state === 'PAUSED' ? 'resume' : 'pause')"
        >
          {{
            selected.state === "PAUSED" ? "继续本次评测" : "暂停本次评测"
          }}</button
        ><button :disabled="busy" class="quiet" @click="control('cancel')">
          取消本次评测
        </button>
      </div>
      <table>
        <thead>
          <tr>
            <th>对照组</th>
            <th>检查通过 / 总次数</th>
            <th>已知实际费用（百万分之一币种）</th>
            <th>已记录执行毫秒</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(label, arm) in armLabels" :key="arm">
            <td>{{ label }}</td>
            <td>{{ summary(arm).passed }} / {{ summary(arm).count }}</td>
            <td>{{ summary(arm).cost }}</td>
            <td>{{ summary(arm).duration }}</td>
          </tr>
        </tbody>
      </table>
      <p>
        当前显示 {{ Math.min(visibleStepCount, selected.steps.length) }} /
        {{ selected.steps.length }} 条执行详情；汇总始终计算全部样例。
      </p>
      <button
        v-if="visibleStepCount < selected.steps.length"
        class="quiet"
        @click="visibleStepCount += 50"
      >
        再显示50条评测详情
      </button>
      <details
        v-for="step in selected.steps.slice(0, visibleStepCount)"
        :key="step.id"
      >
        <summary>
          {{ armLabels[step.arm] }} · {{ step.case_id }} · 第{{
            step.repeat + 1
          }}次 · {{ labels[step.state] || step.state }}
        </summary>
        <p>
          子任务 {{ step.child_id || "尚未执行" }}；费用回执
          {{ step.metrics.cost_known ? "已知" : "未知或未运行" }}；使用量
          {{ step.metrics.usage_known ? "已知" : "未知或未运行" }}；账目版本
          {{ step.metrics.accounting_revision || 0 }}，已留存回执
          {{ step.metrics.receipt_count || 0 }} 条；人工修复
          {{ step.metrics.manual_repairs }} 次
        </p>
        <ul>
          <li v-for="check in step.metrics.score?.checks" :key="check.code">
            {{ check.passed ? "通过" : "未通过" }}：{{ check.detail }}
          </li>
        </ul>
        <pre>{{ step.metrics.output }}</pre>
        <p>输出证据 {{ step.metrics.output_hash || "未取得" }}</p>
      </details>
      <a
        v-if="selected.report_hash"
        :href="`/api${root}/runs/${selected.id}/report`"
        target="_blank"
        rel="noopener noreferrer"
        >打开完整证据报告</a
      >
      <form
        v-if="owner && selected.state === 'REVIEW_REQUIRED'"
        @submit.prevent="approve"
      >
        <label
          >维护检查意见（确认固定依赖、隔离回执和范围）<textarea
            v-model="note"
            required
            rows="3"
          /></label
        ><button :disabled="busy || selected.stale">批准此固定执行依赖</button>
      </form>
      <button
        v-if="owner && selected.state === 'PASSED'"
        :disabled="busy || selected.stale"
        @click="activate(selected)"
      >
        启用此精确机械用途
      </button>
      <details v-if="owner && selected.qualification_id">
        <summary>撤销整个能力版本及依赖用途</summary>
        <p>
          此操作撤销该能力版本以及依赖它的资格和派生版本。仅切换版本请使用下方回退；仅停用某个绑定请使用停用。
        </p>
        <label
          >整个版本的撤销原因<textarea
            v-model="reason"
            rows="2"
            required
          /></label
        ><button :disabled="busy || !reason.trim()" @click="revoke">
          撤销整个能力版本
        </button>
      </details>
    </article>
    <h3 class="qualification-bindings-heading">3. 用途绑定与回退历史</h3>
    <p v-if="!bindings.length">尚无已取得资格的用途绑定。</p>
    <details v-if="owner">
      <summary>项目每周评测（默认关闭付费）</summary>
      <form @submit.prevent="saveWeekly">
        <fieldset aria-label="每周评测固定范围">
          <legend>每周评测固定范围</legend>
          <template v-if="weeklyScope"
            ><p>
              评测集：{{ weeklyScope.suite_id }}；候选版本：{{
                weeklyScope.version_id
              }}；基线：{{ weeklyScope.baseline_id }}
            </p>
            <p>
              故事分支：{{ weeklyScope.branch_id }}；模型用途：{{
                weeklyScope.role
              }}；固定价格卡：{{
                weeklyScope.price_id || "本地工具无收费价格卡"
              }}；精确输出上限：{{ weeklyScope.max_output_tokens }}
            </p></template
          >
          <p v-else>尚未保存周评测范围。先选择上方范围，再明确复制并保存。</p>
          <button
            type="button"
            class="quiet"
            :disabled="busy || !form.version_id || !form.branch_id"
            @click="copyWeeklyScope"
          >
            使用上方评测范围替换周计划
          </button>
        </fieldset>
        <label
          ><input
            v-model="weeklyEnabled"
            :disabled="busy"
            type="checkbox"
          />启用已保存的每周评测范围</label
        ><label
          >每周最多使用的币种金额（0 表示禁止收费）<input
            v-model.number="weeklyLimit"
            :disabled="busy"
            type="number"
            min="0"
            max="1000000"
            step="0.01"
        /></label>
        <p>{{ scheduleReason }}</p>
        <button
          :disabled="
            busy ||
            (weeklyEnabled
              ? !weeklyScope?.version_id || !weeklyScope?.branch_id
              : !scheduleRevision && !weeklyScope)
          "
        >
          保存每周评测设置
        </button>
        <button
          type="button"
          class="quiet"
          :disabled="busy || !weeklyDirty"
          @click="discardWeekly"
        >
          放弃周评测修改
        </button>
      </form>
    </details>
    <article v-for="binding in bindings" :key="binding.id" class="binding">
      <p>
        {{ binding.task_kind }} ·
        {{ binding.current ? "当前选择" : "历史绑定" }} ·
        {{ binding.valid ? "资格仍有效" : "已停用、过期或撤销" }} ·
        {{ binding.id.slice(0, 8) }}
      </p>
      <p>有效期至 {{ binding.qualification?.expires_at }}</p>
      <div v-if="owner" class="actions">
        <button
          :disabled="busy || !binding.valid || binding.current"
          @click="rollback(binding)"
        >
          回退到此仍合格版本</button
        ><button
          class="quiet"
          :disabled="busy || !binding.enabled"
          @click="disable(binding)"
        >
          停用此绑定
        </button>
      </div>
    </article>
  </section>
</template>
<style scoped>
.qualification-panel {
  margin-block: 1.5rem;
}
.qualification-attention {
  margin: 20px 0;
  padding: 16px;
  background: #f6f2e7;
  border: 1px solid #d8d1b7;
  border-radius: 10px;
  line-height: 1.8;
  font-size: 13px;
}
.qualification-history-heading,
.qualification-bindings-heading {
  padding-top: 24px;
  margin-top: 28px;
  border-top: 1px solid #e1e5da;
}
.qualification-history-heading h3 {
  margin: 0;
}
.qualification-filters {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.qualification-runs {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  list-style: none;
  padding: 0;
}
.qualification-runs button[aria-pressed="true"] {
  background: #edf1e6;
  border-color: #91ad98;
}
@media (max-width: 680px) {
  .qualification-filters {
    grid-template-columns: minmax(0, 1fr);
  }
}
fieldset {
  min-width: 0;
}
pre,
p,
li {
  overflow-wrap: anywhere;
}
pre {
  white-space: pre-wrap;
}
table {
  width: 100%;
  border-collapse: collapse;
  margin-block: 1rem;
}
th,
td {
  padding: 0.5rem;
  text-align: left;
  border-bottom: 1px solid #ddd;
}
.binding {
  padding-block: 0.6rem;
  border-bottom: 1px solid #ddd;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.6rem;
}
</style>
