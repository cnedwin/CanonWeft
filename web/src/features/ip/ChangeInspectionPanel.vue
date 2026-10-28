<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
import type { DependencyEntry, DependencyRef } from "./dependencies";
import QualificationPanel from "../skills/QualificationPanel.vue";
import ModelsPage from "../models/ModelsPage.vue";

const props = defineProps<{
  project: Project;
  actor: Actor;
  planId: string;
  sourceBranch: string;
  included: DependencyRef[];
  catalog: DependencyEntry[];
  stale: boolean;
  blocked: boolean;
}>();
const emit = defineEmits<{ expired: []; blocking: [boolean] }>();
type Binding = {
  id: string;
  enabled: boolean;
  current: boolean;
  valid: boolean;
  task_kind: string;
  qualification?: {
    frozen: {
      input: { role: string; max_output_tokens: number };
      config: { id: string; revision: number; name: string };
    };
  };
};
type Option = {
  role: string;
  price: {
    id: string;
    currency: string;
    connection_id: string;
    connection_revision: number;
  };
};
type Task = {
  id: string;
  state: string;
  paused: boolean;
  cancelled: boolean;
  frozen: { estimate_micros: number; price: { currency: string } };
};
type Issue = {
  target_key: string;
  severity: string;
  explanation: string;
  anchor_keys: string[];
};
type Report = {
  generation_id: string;
  plan_id: string;
  state: string;
  current: boolean;
  counted: boolean;
  reason: string;
  scope: string;
  items: { ref: DependencyRef; name: string; state: string; issues: Issue[] }[];
  report?: { verdict: string; summary: string; issues: Issue[] };
  anchors?: { key: string; text: string }[];
};
type Coverage = {
  items: {
    ref: DependencyRef;
    state: string;
    runs: string[];
    checked: boolean;
    pending: boolean;
  }[];
  checked: number;
  pending: number;
  findings: number;
  uncertain: number;
  needs_attention: number;
  unreadable_runs: number;
  outside_scope: number;
  untracked: number;
  automatic_ready: boolean;
  all_listed_checked: boolean;
  next_batch: string[];
};
const coverage = ref<Coverage | null>(null);
const preparingAll = ref(false),
  stopPreparation = ref(false);
const root = `/projects/${props.project.id}`;
const branchID = ref(props.sourceBranch),
  chosen = ref<string[]>([]),
  bindingID = ref("");
const bindings = ref<Binding[]>([]),
  options = ref<Option[]>([]),
  runs = ref<Task[]>([]);
const selected = ref<Task | null>(null),
  report = ref<Report | null>(null),
  next = ref("");
const error = ref(""),
  notice = ref(""),
  busy = ref(false),
  runtimeReady = ref(false),
  operation = ref(newOperationID());
const showQualification = ref(false),
  showModels = ref(false),
  setupDirty = ref(false);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const branches = computed(() => [
  ...new Set(
    props.included.filter((r) => r.kind === "resource").map((r) => r.branch_id),
  ),
]);
const targets = computed(() =>
  props.included.filter(
    (r) => r.kind === "resource" && r.branch_id === branchID.value,
  ),
);
const binding = computed(() =>
  bindings.value.find((b) => b.id === bindingID.value),
);
const option = computed(() =>
  options.value.find(
    (o) =>
      o.role === "critic" &&
      o.price.connection_id ===
        binding.value?.qualification?.frozen.config.id &&
      o.price.connection_revision ===
        binding.value?.qualification?.frozen.config.revision,
  ),
);
const states: Record<string, string> = {
  UNCHECKED: "未计入检查",
  CHECKED_NO_ISSUE_REPORTED: "已检查，本批未报告问题",
  CHECKED_WITH_FINDINGS: "已检查，仍有问题需处理",
  UNCERTAIN: "依据不足，待进一步核查",
  PENDING: "待启动或执行",
  NEEDS_ATTENTION: "未完成有效检查，需手动处理",
  PREPARED: "待启动",
  QUEUED: "等待执行",
  RUNNING: "正在审查",
  SUCCEEDED: "执行完成",
  UNKNOWN: "请求结果未知",
  FAILED: "执行失败",
  CANCELLED: "已取消",
};
let disposed = false,
  epoch = 0;
const pendingInput = ref<Record<string, unknown> | null>(null);
function resetPrepare() {
  operation.value = newOperationID();
  pendingInput.value = null;
}
watch(
  [busy, setupDirty, chosen, pendingInput],
  () =>
    emit(
      "blocking",
      busy.value ||
        setupDirty.value ||
        chosen.value.length > 0 ||
        !!pendingInput.value,
    ),
  { deep: true },
);
watch([chosen, bindingID], resetPrepare, { deep: true });
watch(() => option.value?.price.id, resetPrepare);
watch(branchID, () => {
  epoch++;
  chosen.value = [];
  selected.value = null;
  report.value = null;
  coverage.value = null;
  runs.value = [];
  next.value = "";
  operation.value = newOperationID();
  void refresh();
});
function name(ref: DependencyRef) {
  return (
    props.catalog.find(
      (c) =>
        c.ref.id === ref.id &&
        c.ref.branch_id === ref.branch_id &&
        c.ref.kind === ref.kind,
    )?.name || ref.id
  );
}
function branchName(id: string) {
  return props.catalog.find((c) => c.ref.branch_id === id)?.branch_name || id;
}
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function perform(work: () => Promise<void>) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    busy.value = false;
  }
}
async function loadRuns(append = false) {
  const current = epoch,
    branch = branchID.value,
    cursor = append ? next.value : "";
  if (!branch) return;
  const result = await api<{
    items: Task[];
    next?: string;
    runtime_ready: boolean;
  }>(
    `${root}/changes/${props.planId}/inspections?branch=${encodeURIComponent(branch)}&after=${encodeURIComponent(cursor)}`,
  );
  if (disposed || current !== epoch) return;
  runs.value = append
    ? [
        ...new Map(
          [...runs.value, ...result.items].map((r) => [r.id, r]),
        ).values(),
      ]
    : result.items;
  next.value = result.next || "";
  runtimeReady.value = result.runtime_ready;
}
async function refresh() {
  const current = epoch;
  report.value = null;
  coverage.value = null;
  try {
    const [b, o] = await Promise.all([
      api<{ items: Binding[] }>(`${root}/qualification/bindings`),
      api<{ items: Option[] }>(`${root}/generation-options`),
      loadRuns(),
      loadCoverage(),
    ]);
    if (disposed || current !== epoch) return;
    bindings.value = b.items.filter(
      (v) =>
        v.task_kind === "shared.change-impact" &&
        v.enabled &&
        v.current &&
        v.valid &&
        v.qualification?.frozen.input.role === "critic",
    );
    options.value = o.items;
    if (!bindings.value.some((v) => v.id === bindingID.value))
      bindingID.value = bindings.value[0]?.id || "";
    if (selected.value)
      await read(
        runs.value.find((r) => r.id === selected.value?.id) || selected.value,
      );
  } catch (e) {
    if (!disposed && current === epoch) {
      report.value = null;
      fail(e);
    }
  }
}
async function loadCoverage() {
  const current = epoch;
  if (!branchID.value) return;
  try {
    const result = await api<Coverage>(
      `${root}/changes/${props.planId}/inspection-coverage?branch=${encodeURIComponent(branchID.value)}`,
    );
    if (!disposed && current === epoch) coverage.value = result;
  } catch (e) {
    if (!disposed && current === epoch) coverage.value = null;
    throw e;
  }
}
async function read(task: Task) {
  const current = ++epoch;
  selected.value = task;
  report.value = null;
  try {
    const result = await api<Report>(
      `${root}/generations/${task.id}/change-inspection`,
    );
    if (disposed || current !== epoch) return;
    report.value = result;
    selected.value = { ...task, state: result.state };
  } catch (e) {
    if (!disposed && current === epoch) fail(e);
  }
}
async function openRun(id: string) {
  await perform(async () => {
    const task = await api<Task>(`${root}/generations/${id}`);
    await read(task);
  });
}
async function prepare() {
  await perform(async () => {
    if (
      !canEdit.value ||
      props.stale ||
      !option.value ||
      !binding.value ||
      !chosen.value.length ||
      chosen.value.length > 16
    )
      return;
    const task = await prepareTask(chosen.value);
    if (disposed) return;
    chosen.value = [];
    await Promise.all([loadRuns(), loadCoverage()]);
    await read(task);
  });
}
async function prepareTask(ids: string[]) {
  if (!pendingInput.value) {
    if (!binding.value || !option.value)
      throw new Error("请选择有效审查方法与价格。");
    const branch = await api<{ head: string }>(
      `${root}/branches/${branchID.value}`,
    );
    pendingInput.value = {
      operation_id: operation.value,
      branch_id: branchID.value,
      expected_ip_head: branch.head,
      resource_ids: [...ids].sort(),
      binding_id: binding.value.id,
      price_id: option.value.price.id,
      max_output_tokens:
        binding.value.qualification!.frozen.input.max_output_tokens,
    };
  }
  const task = await api<Task>(
    `${root}/changes/${props.planId}/inspections`,
    "POST",
    pendingInput.value,
  );
  resetPrepare();
  return task;
}
async function prepareAll() {
  await perform(async () => {
    if (
      !canEdit.value ||
      props.stale ||
      !binding.value ||
      !option.value ||
      chosen.value.length
    )
      return;
    preparingAll.value = true;
    stopPreparation.value = false;
    const completed = new Set<string>();
    const selections = new Set<string>();
    try {
      while (!disposed && !stopPreparation.value) {
        // Recover a lost reply using the frozen operation before selecting anything new.
        if (!pendingInput.value) {
          await loadCoverage();
          if (
            !coverage.value?.automatic_ready ||
            !coverage.value.next_batch.length ||
            stopPreparation.value
          )
            break;
        }
        const ids =
          (pendingInput.value?.resource_ids as string[] | undefined) ??
          coverage.value!.next_batch;
        const key = [...ids].sort().join("\n");
        if (selections.has(key))
          throw new Error(
            "覆盖记录尚未更新，已停止准备以避免重复批次。请刷新后继续。",
          );
        const task = await prepareTask(ids);
        selections.add(key);
        completed.add(task.id);
        notice.value = `本次已准备 ${completed.size} 批，未启动模型调用。请逐批查看估算后启动。`;
      }
    } finally {
      preparingAll.value = false;
      if (!disposed) await Promise.all([loadRuns(), loadCoverage()]);
    }
  });
}
async function control(action: string) {
  await perform(async () => {
    if (!selected.value || !canEdit.value) return;
    const task = await api<Task>(
      `${root}/generations/${selected.value.id}/${action === "start" ? "start" : "control"}`,
      "POST",
      action === "start" ? {} : { action },
    );
    await Promise.all([loadRuns(), loadCoverage()]);
    await read(task);
  });
}
async function install() {
  await perform(async () => {
    await api(`${root}/capabilities/presets/change-inspection`, "POST", {});
    notice.value =
      "已安装完整方法、Agent与工作流。安装不等于取得审查资格，请在独立评测中完成专用测试并启用。";
    showQualification.value = true;
  });
}
onMounted(() => {
  if (!branches.value.includes(branchID.value))
    branchID.value = branches.value[0] || "";
  else void refresh();
});
onUnmounted(() => {
  disposed = true;
  stopPreparation.value = true;
  epoch++;
  emit("blocking", false);
});
</script>

<template>
  <section class="panel" aria-label="独立变更影响审查">
    <h3>独立核查变更影响</h3>
    <p>
      从清单中选定一批资料，由独立审查角色比较变更前后和原文。未选资料不计入本批检查；其他资料可先通过上方“补充检查范围”加入。
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="stale" role="status">
      清单版本已改变，请先刷新变更清单再准备新的审查。
    </p>
    <section v-if="coverage" aria-label="跨批审查覆盖">
      <h4>当前分支的跨批审查覆盖</h4>
      <p>
        清单内 {{ coverage.items.length }} 份 · 已检查 {{ coverage.checked }} ·
        待启动或执行 {{ coverage.pending }} · 发现问题 {{ coverage.findings }} ·
        不确定 {{ coverage.uncertain }} · 需手动处理
        {{ coverage.needs_attention }}
      </p>
      <p>
        清单外 {{ coverage.outside_scope }} 份；尚无依赖记录
        {{
          coverage.untracked
        }}
        份（可能与清单内重叠）。这些计数不是全书无遗漏证明，已检查也不等于问题已修复。
      </p>
      <p v-if="coverage.unreadable_runs" role="status">
        有
        {{
          coverage.unreadable_runs
        }}
        条任务的范围无法核实或无权读取，自动准备已停用。
      </p>
      <details>
        <summary>逐份查看覆盖与相关任务</summary>
        <ul>
          <li v-for="item in coverage.items" :key="item.ref.id">
            {{ name(item.ref) }} · {{ states[item.state] || item.state }}
            <button
              v-for="id in item.runs"
              :key="id"
              type="button"
              :disabled="busy || blocked"
              @click="openRun(id)"
            >
              查看任务 {{ id }}
            </button>
          </li>
        </ul>
      </details>
    </section>
    <p v-else>覆盖汇总尚未读取成功；自动准备暂不可用。</p>
    <button
      v-if="preparingAll"
      type="button"
      @click="stopPreparation = true"
      :disabled="stopPreparation"
    >
      {{ stopPreparation ? "当前批次结束后停止" : "停止准备后续批次" }}
    </button>
    <fieldset :disabled="busy || blocked || setupDirty">
      <label
        >审查目标分支<select
          v-model="branchID"
          :disabled="chosen.length > 0 || !!pendingInput"
          aria-label="审查目标分支"
        >
          <option v-for="id in branches" :key="id" :value="id">
            {{ branchName(id) }}
          </option>
        </select></label
      >
      <p v-if="!targets.length">该分支清单中尚无可检查的已发布资料。</p>
      <label
        v-for="target in targets"
        :key="target.id"
        class="inspection-target"
        ><input
          v-model="chosen"
          type="checkbox"
          :value="target.id"
          :disabled="
            !canEdit ||
            stale ||
            (chosen.length >= 16 && !chosen.includes(target.id))
          "
        />{{ name(target) }} · 版本 {{ target.revision }}</label
      >
      <p>已选 {{ chosen.length }} / {{ targets.length }} 份，本批最多16份。</p>
      <button type="button" :disabled="!chosen.length" @click="chosen = []">
        清空本批选择
      </button>
      <label
        >合格影响审查方法<select
          v-model="bindingID"
          aria-label="合格影响审查方法"
        >
          <option value="">尚未选择</option>
          <option v-for="b in bindings" :key="b.id" :value="b.id">
            {{ b.qualification?.frozen.config.name }} · {{ b.id }}
          </option>
        </select></label
      >
      <p v-if="!option">
        需要已通过“固定变更影响独立审查”测试的方法，以及匹配的审查模型和有效价格。一般一致性审查资格不能代替。
      </p>
      <button
        type="button"
        :disabled="!canEdit || stale || !chosen.length || !option"
        @click="prepare"
      >
        准备本批审查并估算费用
      </button>
      <button type="button" @click="refresh">刷新审查条件与记录</button>
      <button
        type="button"
        :disabled="
          !canEdit ||
          stale ||
          !option ||
          chosen.length > 0 ||
          (!pendingInput &&
            (!coverage?.automatic_ready || !coverage.next_batch.length))
        "
        @click="prepareAll"
      >
        自动准备未检查批次
      </button>
      <p>
        自动准备每批最多16份，只选择从未尝试的资料。失败、结果未知或资格失效的任务需查看后手动处理；准备后逐批显示估算，不会自动启动模型。
      </p>
      <button v-if="canEdit" type="button" @click="install">
        安装预置影响审查方法
      </button>
      <button
        v-if="canEdit"
        type="button"
        @click="showQualification = !showQualification"
      >
        {{ showQualification ? "收起" : "打开" }}专用审查评测
      </button>
      <button
        v-if="actor.role === 'owner'"
        type="button"
        @click="showModels = !showModels"
      >
        {{ showModels ? "收起" : "打开" }}审查模型设置
      </button>
    </fieldset>
    <QualificationPanel
      v-if="showQualification"
      :actor="actor"
      :project="project"
      initial-suite="shared-change-impact-v1"
      @blocking="setupDirty = $event"
      @expired="emit('expired')"
    />
    <ModelsPage
      v-if="showModels"
      @close="
        showModels = false;
        refresh();
      "
      @expired="emit('expired')"
    />
    <p class="muted">
      以下为可读的审查任务，分页记录不是全清单通过证明。未启动不产生模型费用，启动仍受后台预算与权限限制。
    </p>
    <button
      v-for="task in runs"
      :key="task.id"
      type="button"
      :disabled="busy || blocked"
      @click="read(task)"
    >
      {{ states[task.state] || task.state }} · {{ task.id }} · 估算
      {{ (task.frozen.estimate_micros / 1000000).toFixed(6) }}
      {{ task.frozen.price.currency }}
    </button>
    <button
      v-if="next"
      type="button"
      :disabled="busy || blocked"
      @click="perform(() => loadRuns(true))"
    >
      继续加载审查任务
    </button>
    <section v-if="selected" aria-label="本批审查结果">
      <p>
        任务 {{ selected.id }} · 估算
        {{ (selected.frozen.estimate_micros / 1000000).toFixed(6) }}
        {{ selected.frozen.price.currency }}
      </p>
      <button
        v-if="selected.state === 'PREPARED'"
        type="button"
        :disabled="busy || blocked || stale || !canEdit || !runtimeReady"
        @click="control('start')"
      >
        按估算启动审查
      </button>
      <button
        v-if="['QUEUED', 'RUNNING'].includes(selected.state)"
        type="button"
        :disabled="busy || blocked || !canEdit"
        @click="control(selected.paused ? 'resume' : 'pause')"
      >
        {{ selected.paused ? "继续审查" : "暂停审查" }}
      </button>
      <button
        v-if="
          ['PREPARED', 'QUEUED', 'RUNNING', 'UNKNOWN'].includes(selected.state)
        "
        type="button"
        :disabled="busy || blocked || !canEdit"
        @click="control('cancel')"
      >
        取消本批审查
      </button>
      <button
        type="button"
        :disabled="busy || blocked"
        @click="perform(refresh)"
      >
        刷新本批结果
      </button>
      <template v-if="report">
        <p>{{ report.reason }}</p>
        <p class="muted">{{ report.scope }}</p>
        <p v-if="report.report">{{ report.report.summary }}</p>
        <article v-for="item in report.items" :key="item.ref.id">
          <strong>{{ item.name }}</strong> ·
          {{ states[item.state] || item.state
          }}<code>{{ item.ref.revision }}</code>
        </article>
        <article
          v-for="(issue, index) in report.report?.issues || []"
          :key="index"
        >
          <p>
            {{
              issue.severity === "blocker"
                ? "明确冲突"
                : issue.severity === "unknown"
                  ? "无法确定"
                  : "注意事项"
            }}
            · {{ issue.explanation }}
          </p>
          <details v-for="key in issue.anchor_keys" :key="key">
            <summary>查看依据 {{ key }}</summary>
            <pre>{{ report.anchors?.find((a) => a.key === key)?.text }}</pre>
          </details>
        </article>
      </template>
    </section>
  </section>
</template>

<style scoped>
.inspection-target {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  overflow-wrap: anywhere;
}
.inspection-target input {
  width: auto;
}
fieldset {
  display: grid;
  gap: 0.6rem;
}
code {
  display: block;
  overflow-wrap: anywhere;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
