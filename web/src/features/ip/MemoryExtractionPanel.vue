<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
import type { Branch } from "./types";
import {
  memoryKinds,
  summaryLevels,
  type SummaryLevel,
  type MemoryItem,
  type MemoryRefreshTask,
  type MemoryKind,
  type MemoryResource,
} from "./memory";
import { recipeStates, type RecipeRun } from "../skills/recipeTypes";

const props = defineProps<{
  actor: Actor;
  project: Project;
  branch: Branch;
  resources: MemoryResource[];
  items: MemoryItem[];
  refreshTasks: MemoryRefreshTask[];
  sessionId: string;
  blocked: boolean;
}>();
const emit = defineEmits<{
  imported: [];
  expired: [];
  blocking: [boolean];
  attention: [count: number];
}>();
const panelElement = ref<HTMLDetailsElement | null>(null);
const recordSelect = ref<HTMLSelectElement | null>(null);
async function focusTasks() {
  if (!panelElement.value) return;
  panelElement.value.open = true;
  await nextTick();
  recordSelect.value?.focus({ preventScroll: true });
  recordSelect.value?.scrollIntoView({ block: "center" });
}
defineExpose({ focusTasks });
type Option = {
  role: string;
  name: string;
  model: string;
  price: { id: string; currency: string };
};
type Candidate = {
  id: string;
  kind: string;
  statement: string;
  context: string;
  disposition: string;
  evidence: { quote: string }[];
};
const base = `/projects/${props.project.id}`;
const root = `${base}/branches/${props.branch.id}/memory`;
const storageKey = `memory-extraction:${props.actor.id}:${props.project.id}:${props.branch.id}`;
const resourceIDs = ref<string[]>([]),
  instructions = ref(""),
  storyTime = ref(0),
  budget = ref("");
const summaryLevel = ref<SummaryLevel | "">("");
const refreshID = ref("");
const refreshTask = computed(() =>
  props.refreshTasks.find((task) => task.id === refreshID.value),
);
function selectRefreshTask() {
  const task = refreshTask.value;
  if (!task) {
    changeSummaryLevel();
    return;
  }
  const target = task.input.summary_target;
  summaryLevel.value = target.level;
  summaryTitle.value = target.title;
  summaryUnit.value = target.unit_id;
  resourceIDs.value = [...(task.input.resource_ids ?? [])];
  memoryIDs.value = [...(task.input.memory_ids ?? [])];
  fromSummaries.value = memoryIDs.value.length > 0;
  storyTime.value = task.input.story_time;
}
const summaryTitle = ref("");
const summaryUnit = ref(crypto.randomUUID().replaceAll("-", ""));
const memoryIDs = ref<string[]>([]);
const fromSummaries = ref(false);
const usesSummaries = computed(
  () =>
    ["volume", "book"].includes(summaryLevel.value) ||
    (summaryLevel.value === "chapter" && fromSummaries.value),
);
const lowerSummaries = computed(() =>
  props.items.filter(
    (item) =>
      ["published", "adopted_plan"].includes(item.state) &&
      item.summary?.level ===
        (
          { chapter: "scene", volume: "chapter", book: "volume" } as Record<
            string,
            string
          >
        )[summaryLevel.value] &&
      !item.save &&
      (item.timeline || "main") ===
        (refreshTask.value?.input.summary_target.timeline ?? "main"),
  ),
);
const hasSources = computed(() =>
  usesSummaries.value
    ? memoryIDs.value.length > 0
    : resourceIDs.value.length > 0,
);
function changeSummaryLevel() {
  memoryIDs.value = [];
  summaryUnit.value = crypto.randomUUID().replaceAll("-", "");
}
const options = ref<Option[]>([]),
  priceID = ref("");
const runs = ref<RecipeRun[]>([]),
  selected = ref<RecipeRun | null>(null),
  selectedID = ref("");
function needsAttention(run: RecipeRun) {
  return (
    run.state === "UNKNOWN" ||
    run.uncertain_calls > 0 ||
    !!run.steps?.some((step) => step.active && step.state === "UNKNOWN")
  );
}
const uncertainRuns = computed(() => {
  const known = new Map(runs.value.map((run) => [run.id, run]));
  if (selected.value) known.set(selected.value.id, selected.value);
  return [...known.values()].filter(needsAttention);
});
watch(
  () => uncertainRuns.value.length,
  (count) => emit("attention", count),
);
const chosen = ref<string[]>([]),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const locked = computed(() => busy.value || props.blocked);
const stage = computed(() =>
  selected.value?.steps?.find(
    (s) => s.active && s.key === "memory-consolidation",
  ),
);
const candidates = computed(
  () =>
    (stage.value?.output as { candidates?: Candidate[] } | undefined)
      ?.candidates ?? [],
);
const canImport = computed(
  () =>
    !!stage.value?.output_hash &&
    ["WAITING_CONFIRMATION", "COMPLETED"].includes(
      selected.value?.state ?? "",
    ) &&
    !selected.value?.paused &&
    !selected.value?.cancel_requested &&
    !selected.value?.restored_disabled,
);
const proposalNotes = computed(
  () =>
    stage.value?.output as
      | {
          findings?: { description: string; question: string }[];
          limitations?: string[];
        }
      | undefined,
);
const contextLabels: Record<string, string> = {
  reality: "现实",
  dream: "梦境",
  flashback: "回忆",
  hypothetical: "假设",
  plan: "未发生的计划",
};
const dispositionLabels: Record<string, string> = {
  proposed: "建议候选",
  conflict: "存在冲突",
  needs_confirmation: "需要确认",
};
function kindLabel(kind: string) {
  return (
    memoryKinds[kind as MemoryKind] ??
    ({ foreshadow: "伏笔", theme_check: "主题核对" } as Record<string, string>)[
      kind
    ] ??
    kind
  );
}
const selectionPrice = computed(() =>
  options.value.find((o) => o.price.id === priceID.value),
);
let disposed = false,
  polling = false,
  initialized = false,
  timer: ReturnType<typeof setInterval> | undefined;
let preparedRequest: { signature: string; id: string } | null = null;
let preparationContext: { signature: string; id: string } | null = null;
watch(busy, (value) => emit("blocking", value));
watch(
  [
    resourceIDs,
    instructions,
    storyTime,
    budget,
    priceID,
    summaryLevel,
    summaryTitle,
    summaryUnit,
    memoryIDs,
    fromSummaries,
    refreshID,
  ],
  () => {
    try {
      localStorage.setItem(
        storageKey,
        JSON.stringify({
          resourceIDs: resourceIDs.value,
          instructions: instructions.value,
          storyTime: storyTime.value,
          budget: budget.value,
          priceID: priceID.value,
          summaryLevel: summaryLevel.value,
          summaryTitle: summaryTitle.value,
          summaryUnit: summaryUnit.value,
          memoryIDs: memoryIDs.value,
          fromSummaries: fromSummaries.value,
          refreshID: refreshID.value,
        }),
      );
    } catch {
      error.value = "整理要求未能暂存，请保留文字后再离开。";
    }
  },
  { deep: true },
);
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 409) preparationContext = null;
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function perform(work: () => Promise<void>) {
  if (locked.value) return;
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
async function readRun(id: string) {
  if (!id) {
    selected.value = null;
    chosen.value = [];
    return;
  }
  const result = await api<RecipeRun>(`${base}/recipe-executions/${id}`);
  if (disposed || selectedID.value !== id) return;
  if (selected.value?.id === id && result.revision < selected.value.revision)
    return;
  const previous = stage.value?.output_hash;
  selected.value = result;
  runs.value = runs.value.map((run) => (run.id === id ? result : run));
  if (previous !== stage.value?.output_hash) chosen.value = [];
}
async function refresh() {
  const [list, modelOptions] = await Promise.all([
    api<{ items: RecipeRun[] }>(
      `${base}/recipe-executions?branch_id=${props.branch.id}`,
    ),
    api<{ items: Option[] }>(`${base}/generation-options`),
  ]);
  if (disposed) return;
  runs.value = list.items.filter(
    (r) =>
      r.frozen?.definition.definition.key ===
      "shared-memory-consolidation-recipe",
  );
  options.value = modelOptions.items.filter((o) => o.role === "bookkeeper");
  if (!priceID.value) priceID.value = options.value[0]?.price.id ?? "";
  if (selectedID.value) await readRun(selectedID.value);
}
async function prepare() {
  await perform(async () => {
    const price = selectionPrice.value;
    const amount = Number(budget.value);
    if (refreshID.value && refreshTask.value?.state !== "READY")
      throw new Error("重整条件已变化，请刷新记忆后重新选择可处理的摘要。");
    if (!price || !Number.isFinite(amount) || amount <= 0 || amount > 1000000)
      throw new Error("请选择整理模型并填写本次预算上限。");
    if (!props.branch.head || !hasSources.value)
      throw new Error("先确认当前设定，并选择已经发布的作品资料。");
    if (summaryLevel.value && !summaryTitle.value.trim())
      throw new Error("请填写本次摘要的名称。");
    if (
      usesSummaries.value &&
      memoryIDs.value.some(
        (id) => !lowerSummaries.value.some((item) => item.id === id),
      )
    )
      throw new Error("选择的下层摘要已变更，请重新选择当前层级的已采用摘要。");
    const input = await api<unknown>(`${root}/extraction-input`, "POST", {
      resource_ids: usesSummaries.value ? [] : resourceIDs.value,
      ...(usesSummaries.value ? { memory_ids: memoryIDs.value } : {}),
      ...(summaryLevel.value
        ? {
            summary_target: refreshTask.value?.input.summary_target ?? {
              level: summaryLevel.value,
              title: summaryTitle.value.trim(),
              unit_id: summaryUnit.value,
              timeline: "main",
            },
          }
        : {}),
      expected_ip_head: props.branch.head,
      story_time: storyTime.value,
      focus: [
        "facts",
        "beliefs",
        "relations",
        "state",
        "summaries",
        "foreshadow",
        "highlights",
        "theme",
      ],
      instructions: instructions.value,
    });
    if (disposed) return;
    const installed = await api<{ root_id: string }>(
      `${base}/capabilities/presets/memory`,
      "POST",
      {},
    );
    if (disposed) return;
    const contextSignature = JSON.stringify([
      input,
      price.price.id,
      budget.value,
    ]);
    if (preparationContext?.signature !== contextSignature) {
      const context = await api<{ id: string }>(
        `${base}/branches/${props.branch.id}/knowledge/search`,
        "POST",
        {
          query: instructions.value.trim() || "人物关系、章节摘要、伏笔与主题",
          purpose: "critic",
          story_time: storyTime.value,
          limit: 3,
          token_budget: 65536,
          ...(refreshTask.value
            ? {
                memory: {
                  timeline: refreshTask.value.input.summary_target.timeline,
                  refresh: {
                    id: refreshTask.value.input.summary_target.item_id,
                    revision: refreshTask.value.input.summary_target.revision,
                  },
                },
              }
            : {}),
        },
      );
      if (disposed) return;
      preparationContext = { signature: contextSignature, id: context.id };
    }
    if (disposed) return;
    const payload = {
      recipe_version_id: installed.root_id,
      branch_id: props.branch.id,
      expected_ip_head: props.branch.head,
      trial: true,
      input,
      currency: price.price.currency,
      budget_limit_micros: Math.round(amount * 1000000),
      steps: [
        {
          key: "memory-consolidation",
          price_id: price.price.id,
          max_output_tokens: 8192,
          context_id: preparationContext.id,
          input,
          bindings: [],
        },
      ],
    };
    const signature = JSON.stringify(payload);
    if (preparedRequest?.signature !== signature)
      preparedRequest = { signature, id: newOperationID() };
    const run = await api<RecipeRun>(`${base}/recipe-executions`, "POST", {
      ...payload,
      operation_id: preparedRequest.id,
    });
    if (disposed) return;
    selectedID.value = run.id;
    selected.value = run;
    chosen.value = [];
    notice.value =
      "资料已准备，尚未调用模型。开始后将使用后台模型与预算设置，输出仍需你审阅。";
    await refresh();
  });
}
async function action(name: "start" | "cancel" | "confirm") {
  await perform(async () => {
    const run = selected.value;
    if (!run) return;
    const result = await api<RecipeRun>(
      `${base}/recipe-executions/${run.id}/${name === "cancel" ? "control" : name}`,
      "POST",
      {
        expected_revision: run.revision,
        ...(name === "cancel" ? { action: "cancel" } : {}),
        ...(name === "confirm"
          ? {
              step_key: stage.value?.key,
              output_hash: stage.value?.output_hash,
            }
          : {}),
      },
    );
    if (disposed) return;
    selected.value = result;
    await readRun(run.id);
  });
}
async function importCandidates() {
  await perform(async () => {
    if (
      !selected.value ||
      !stage.value ||
      !props.sessionId ||
      chosen.value.length === 0
    )
      return;
    const target = (
      stage.value.frozen?.input?.input as
        | { summary_target?: { item_id?: string; timeline?: string } }
        | undefined
    )?.summary_target;
    if (target?.item_id && chosen.value.length !== 1)
      throw new Error("重整同一摘要时，请只选择一个版本作为替换候选。");
    await api(`${root}/import-extraction`, "POST", {
      run_id: selected.value.id,
      step_id: stage.value.id,
      expected_revision: selected.value.revision,
      output_hash: stage.value.output_hash,
      session_id: props.sessionId,
      timeline: target?.timeline ?? "main",
      candidate_ids: chosen.value,
    });
    if (disposed) return;
    notice.value =
      "已加入当前私人会话。可在记忆浏览中编辑内容、核对来源，再逐项采用。重复导入不会覆盖你的修改。";
    preparationContext = null;
    emit("imported");
  });
}
function openPanel(event: Event) {
  if ((event.target as HTMLDetailsElement).open && !initialized) {
    initialized = true;
    void perform(refresh);
  }
}
onMounted(() => {
  try {
    const draft = JSON.parse(localStorage.getItem(storageKey) ?? "null") as {
      resourceIDs?: string[];
      instructions?: string;
      storyTime?: number;
      budget?: string;
      priceID?: string;
      summaryLevel?: SummaryLevel | "";
      summaryTitle?: string;
      summaryUnit?: string;
      memoryIDs?: string[];
      fromSummaries?: boolean;
      refreshID?: string;
    } | null;
    if (draft) {
      resourceIDs.value = draft.resourceIDs ?? [];
      instructions.value = draft.instructions ?? "";
      storyTime.value = draft.storyTime ?? 0;
      budget.value = draft.budget ?? "";
      priceID.value = draft.priceID ?? "";
      summaryLevel.value = draft.summaryLevel ?? "";
      summaryTitle.value = draft.summaryTitle ?? "";
      summaryUnit.value = draft.summaryUnit ?? summaryUnit.value;
      memoryIDs.value = draft.memoryIDs ?? [];
      fromSummaries.value = draft.fromSummaries ?? false;
      refreshID.value = draft.refreshID ?? "";
    }
  } catch {
    error.value = "暂存要求无法读取，请重新填写。";
  }
  timer = setInterval(() => {
    if (
      disposed ||
      polling ||
      busy.value ||
      !selectedID.value ||
      selected.value?.state !== "RUNNING"
    )
      return;
    polling = true;
    void readRun(selectedID.value)
      .catch((e) => {
        if (!disposed) fail(e);
      })
      .finally(() => {
        polling = false;
      });
  }, 5000);
});
onBeforeUnmount(() => {
  disposed = true;
  if (timer) clearInterval(timer);
  emit("blocking", false);
  emit("attention", 0);
});
</script>

<template>
  <details
    ref="panelElement"
    class="extraction-panel"
    data-testid="memory-extraction"
    @toggle="openPanel"
  >
    <summary>从作品资料整理记忆</summary>
    <p>
      提炼人物信息、章节摘要、伏笔和高光，保留来源。结果先进入你的私人候选，不会自动改变正史。
    </p>
    <aside
      v-if="uncertainRuns.length"
      class="author-status"
      aria-label="已读取的待核对记忆整理任务"
    >
      <p>
        已读取的整理任务中有
        {{ uncertainRuns.length }}
        项结果或费用需核对。不自动重发，已有费用占用保留。
      </p>
      <button
        v-for="run in uncertainRuns"
        :key="run.id"
        type="button"
        class="quiet"
        :disabled="locked"
        @click="
          selectedID = run.id;
          perform(() => readRun(run.id));
        "
      >
        查看待核对整理任务 {{ run.id.slice(0, 8) }}
      </button>
    </aside>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <fieldset :disabled="locked">
      <legend>选择已经发布的资料</legend>
      <label
        >增量重整<select v-model="refreshID" @change="selectRefreshTask">
          <option value="">新增整理，不替换已有摘要</option>
          <option
            v-for="task in refreshTasks"
            :key="task.id"
            :value="task.id"
            :disabled="task.state !== 'READY'"
          >
            {{ task.input.summary_target.title }} ·
            {{ task.state === "READY" ? "可以重新整理" : task.reason }}
          </option>
          <option v-if="refreshID && !refreshTask" :value="refreshID" disabled>
            原待办已变化，请重新选择
          </option>
        </select></label
      >
      <p v-if="refreshTasks.length">
        按下层到上层处理；下层确认后，上层才可以继续。待办由已保存的来源和记忆修订重建，尚未自动调用模型。
      </p>
      <p v-if="refreshID">
        重整将保留原摘要身份和核心约束，先生成替换候选，仍需你确认采用。
      </p>
      <label
        >整理方式<select
          v-model="summaryLevel"
          :disabled="!!refreshID"
          @change="changeSummaryLevel"
        >
          <option value="">综合提炼记忆</option>
          <option
            v-for="(name, level) in summaryLevels"
            :key="level"
            :value="level"
          >
            {{ name }}摘要
          </option>
        </select></label
      >
      <label v-if="summaryLevel"
        >摘要名称<input
          v-model="summaryTitle"
          :disabled="!!refreshID"
          maxlength="160"
          placeholder="例如：第一卷·重返雾城"
      /></label>
      <label v-if="summaryLevel === 'chapter'"
        ><input
          v-model="fromSummaries"
          :disabled="!!refreshID"
          type="checkbox"
        />从已采用的场景摘要整理</label
      >
      <template v-if="usesSummaries">
        <p>
          选择相邻下层的已采用摘要。范围仅包括所选内容，不代表已经覆盖全书。
        </p>
        <label v-for="item in lowerSummaries" :key="item.id" class="choice"
          ><input
            v-model="memoryIDs"
            type="checkbox"
            :value="item.id"
            :disabled="!!refreshID"
          />{{ item.summary?.title }}</label
        >
        <p v-if="!lowerSummaries.length">请先生成并采用相邻下层的摘要。</p>
      </template>
      <template v-else>
        <label v-for="resource in resources" :key="resource.id" class="choice"
          ><input
            v-model="resourceIDs"
            :disabled="!!refreshID"
            type="checkbox"
            :value="resource.id"
          />{{ resource.name }}</label
        >
        <p v-if="!resources.length">发布章节或规划资料后，即可在这里选择。</p>
      </template>
      <label
        >整理要求<textarea
          v-model="instructions"
          maxlength="8000"
          placeholder="例如：保留人物不知道的秘密，找出本章伏笔和情感高光。"
        />
      </label>
      <label
        >整理到的故事时间<input
          v-model.number="storyTime"
          type="number"
          min="0"
          step="1"
      /></label>
      <label
        >整理模型<select v-model="priceID">
          <option value="">请选择</option>
          <option
            v-for="option in options"
            :key="option.price.id"
            :value="option.price.id"
          >
            {{ option.name }} · {{ option.model }}
          </option>
        </select></label
      >
      <p v-if="!options.length">
        请先在模型设置中为资料记账角色绑定模型，并设置有效价格。
      </p>
      <label
        >本次预算上限（{{
          selectionPrice?.price.currency ?? "所选模型币种"
        }}）<input v-model="budget" type="number" min="0.000001" step="0.01"
      /></label>
      <button
        type="button"
        :disabled="!hasSources || !priceID"
        @click="prepare"
      >
        准备整理任务
      </button>
    </fieldset>
    <div class="actions">
      <button
        type="button"
        class="quiet"
        :disabled="locked"
        @click="perform(refresh)"
      >
        刷新整理记录
      </button>
      <select
        ref="recordSelect"
        v-model="selectedID"
        aria-label="选择整理记录"
        :disabled="locked"
        @change="perform(() => readRun(selectedID))"
      >
        <option value="">请选择记录</option>
        <option v-for="run in runs" :key="run.id" :value="run.id">
          {{ recipeStates[run.state] ?? run.state }} · {{ run.id.slice(0, 8) }}
        </option>
      </select>
    </div>
    <section v-if="selected">
      <p>
        {{ recipeStates[selected.state] ?? selected.state }} · 上限
        {{ selected.budget_limit_micros / 1000000 }} {{ selected.currency }} ·
        已记账 {{ selected.spent_micros / 1000000 }} {{ selected.currency }}
      </p>
      <p
        v-if="selected.held_micros || selected.uncertain_calls"
        class="author-status"
      >
        当前任务占用 {{ (selected.held_micros ?? 0) / 1000000 }}
        {{ selected.currency }} · 结果或用量未明确的调用
        {{ selected.uncertain_calls ?? 0 }} 次
      </p>
      <p v-if="needsAttention(selected)" class="author-status">
        请求结果或用量尚不明确，请先核对任务与账单；不自动重发或释放已有费用占用。
      </p>
      <p v-if="selected.reason">
        暂未继续：{{
          selected.reason
        }}。可检查模型、预算和资料版本；结果未知时请先核对账单。
      </p>
      <button
        v-if="selected.state === 'PREPARED'"
        type="button"
        :disabled="locked"
        @click="action('start')"
      >
        按已设预算开始整理
      </button>
      <button
        v-if="!['COMPLETED', 'CANCELLED'].includes(selected.state)"
        type="button"
        class="quiet"
        :disabled="locked"
        @click="action('cancel')"
      >
        取消此任务
      </button>
      <article v-for="candidate in candidates" :key="candidate.id">
        <label class="choice"
          ><input
            v-model="chosen"
            type="checkbox"
            :value="candidate.id"
            :disabled="locked || !canImport"
          />{{ candidate.statement }}</label
        >
        <p>
          {{ kindLabel(candidate.kind) }} ·
          {{ contextLabels[candidate.context] ?? candidate.context }} ·
          {{
            dispositionLabels[candidate.disposition] ?? candidate.disposition
          }}
        </p>
        <blockquote
          v-for="(evidence, index) in candidate.evidence"
          :key="index"
        >
          {{ evidence.quote }}
        </blockquote>
      </article>
      <button
        v-if="canImport && selected.state === 'WAITING_CONFIRMATION'"
        type="button"
        class="quiet"
        :disabled="locked"
        @click="action('confirm')"
      >
        完成本次整理审阅（不自动采用）
      </button>
      <aside
        v-if="
          proposalNotes?.findings?.length || proposalNotes?.limitations?.length
        "
      >
        <h4>需要核对的内容</h4>
        <p
          v-for="(finding, index) in proposalNotes?.findings"
          :key="'finding-' + index"
        >
          {{ finding.description }} {{ finding.question }}
        </p>
        <p
          v-for="(limit, index) in proposalNotes?.limitations"
          :key="'limit-' + index"
        >
          {{ limit }}
        </p>
      </aside>
      <p v-if="canImport && !sessionId">请先在上方选择或新建私人工作会话。</p>
      <button
        v-if="canImport"
        type="button"
        :disabled="
          locked || !sessionId || !chosen.length || chosen.length > 128
        "
        @click="importCandidates"
      >
        将选中结果加入私人候选
      </button>
    </section>
  </details>
</template>

<style scoped>
.extraction-panel {
  border: 1px solid var(--border, #ddd);
  padding: 1rem;
  margin-block: 1rem;
}
fieldset {
  display: grid;
  gap: 0.75rem;
}
label {
  display: grid;
  gap: 0.35rem;
}
.choice {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
}
article {
  margin-block: 1rem;
  border-top: 1px solid var(--border, #ddd);
  padding-top: 0.75rem;
}
blockquote {
  white-space: pre-wrap;
}
</style>
