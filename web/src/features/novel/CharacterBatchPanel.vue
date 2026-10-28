<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Project,
} from "../../api";
import type { NovelBook } from "./types";
import type {
  CharacterDesignDraft,
  CharacterDesignSuggestion,
} from "./characterTypes";
import {
  recipeStates,
  type RecipeRun,
  type RecipeStage,
} from "../skills/recipeTypes";
import type { CapabilityVersion } from "../skills/capabilityTypes";

const props = defineProps<{
  book: NovelBook;
  project: Project;
  draft: CharacterDesignDraft;
}>();
const emit = defineEmits<{
  expired: [];
  blocking: [boolean];
  suggestion: [CharacterDesignSuggestion];
}>();
type Report = {
  run: RecipeRun;
  completed: number;
  total: number;
  stale: boolean;
  stale_reason: string;
  suggestion?: CharacterDesignSuggestion;
  passes: {
    key: string;
    ids: string[];
    result: {
      entities: { id: string; name: string; description: string }[];
      explanation: string;
      warnings: string[];
    };
  }[];
};
type ModelOption = {
  role: string;
  name: string;
  model: string;
  price: { id: string; currency: string };
};
const root = `/novels/${props.book.id}/character-designs/${props.draft.resource_id}/batches`;
const recipes = `/projects/${props.project.id}/recipe-executions`;
const notes = ref(props.draft.document.notes),
  size = ref(5),
  tokens = ref(8192),
  budget = ref(""),
  agent = ref("");
const operation = ref(newOperationID()),
  busy = ref(false),
  dirty = ref(false),
  error = ref(""),
  notice = ref("");
const report = ref<Report | null>(null),
  runs = ref<RecipeRun[]>([]),
  models = ref<ModelOption[]>([]),
  agents = ref<CapabilityVersion[]>([]),
  cursor = ref("");
const model = computed(() =>
  models.value.find((o) => o.role === "character_designer"),
);
const stages = computed(() =>
  (report.value?.run.steps ?? []).filter((s) => s.active),
);
const passCount = computed(() =>
  Math.ceil(props.draft.document.ids.length / size.value),
);
let alive = true,
  timer: ReturnType<typeof setTimeout> | undefined;
watch(
  [notes, size, tokens, budget, agent],
  () => {
    operation.value = newOperationID();
    dirty.value = true;
  },
  { flush: "sync" },
);
watch([busy, dirty], () => emit("blocking", busy.value || dirty.value), {
  immediate: true,
});
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function perform(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await action();
  } catch (e) {
    if (alive) fail(e);
  } finally {
    if (alive) busy.value = false;
  }
}
function matchingAgents(items: CapabilityVersion[]) {
  return items.filter(
    (v) =>
      v.definition.kind === "agent" &&
      v.definition.role === "character_designer" &&
      Object.hasOwn(
        (v.definition.input_schema.properties ?? {}) as object,
        "previous",
      ),
  );
}
async function moreAgents() {
  await perform(async () => {
    const page = await api<{ items: CapabilityVersion[]; next_cursor: string }>(
      `/projects/${props.project.id}/capabilities?after=${encodeURIComponent(cursor.value)}`,
    );
    if (alive) {
      agents.value.push(...matchingAgents(page.items));
      cursor.value = page.next_cursor;
    }
  });
}
async function refresh() {
  const list = await api<{ items: RecipeRun[] }>(root);
  if (!alive) return;
  runs.value = list.items;
  const id = report.value?.run.id;
  if (id) {
    const value = await api<Report>(`${root}/${id}`);
    if (alive && report.value?.run.id === id) report.value = value;
  }
}
async function choose(id: string) {
  await perform(async () => {
    const value = await api<Report>(`${root}/${id}`);
    if (alive) report.value = value;
  });
}
async function prepare() {
  await perform(async () => {
    if (!model.value)
      throw new Error("请先在模型后台绑定角色设计用途，并填写有效价格。");
    const amount = Math.round(Number(budget.value) * 1e6);
    if (
      !Number.isSafeInteger(amount) ||
      amount < 1 ||
      !notes.value.trim() ||
      passCount.value > 32
    )
      throw new Error("请填写创作要求、正数费用上限，并使分段数量不超过32段。");
    const run = await api<RecipeRun>(root, "POST", {
      revision: props.draft.revision,
      hash: props.draft.hash,
      notes: notes.value,
      size: size.value,
      price_id: model.value.price.id,
      currency: model.value.price.currency,
      budget_limit_micros: amount,
      max_output_tokens: tokens.value,
      agent_version_id: agent.value,
      operation_id: operation.value,
    });
    if (!alive) return;
    const value = await api<Report>(`${root}/${run.id}`);
    if (alive) {
      report.value = value;
      dirty.value = false;
      notice.value = "分段任务已保存，尚未调用模型。";
      await refresh();
    }
  });
}
async function control(action: string) {
  await perform(async () => {
    if (!report.value) return;
    const run = report.value.run;
    await api(
      `${recipes}/${run.id}/${action === "start" ? "start" : "control"}`,
      "POST",
      {
        expected_revision: run.revision,
        ...(action === "start" ? {} : { action }),
      },
    );
    await refresh();
  });
}
async function retry(stage: RecipeStage) {
  await perform(async () => {
    if (!report.value) return;
    await api(`${recipes}/${report.value.run.id}/retry`, "POST", {
      expected_revision: report.value.run.revision,
      operation_id: newOperationID(),
      step: stage.frozen.input,
    });
    notice.value =
      "已建立本段的新尝试；已完成前段保留。后续依赖本段的结果会重新生成。";
    await refresh();
  });
}
function reset() {
  notes.value = props.draft.document.notes;
  size.value = 5;
  tokens.value = 8192;
  budget.value = "";
  agent.value = "";
  dirty.value = false;
}
function schedule() {
  timer = setTimeout(async () => {
    if (!alive) return;
    if (
      !busy.value &&
      report.value?.run.state === "RUNNING" &&
      !report.value.run.paused
    ) {
      try {
        await refresh();
      } catch (e) {
        if (alive) fail(e);
      }
    }
    if (alive) schedule();
  }, 3000);
}
onMounted(
  () =>
    void perform(async () => {
      const [options, versions] = await Promise.all([
        api<{ items: ModelOption[] }>(
          `/projects/${props.project.id}/generation-options`,
        ),
        api<{ items: CapabilityVersion[]; next_cursor: string }>(
          `/projects/${props.project.id}/capabilities`,
        ),
      ]);
      if (!alive) return;
      models.value = options.items;
      agents.value = matchingAgents(versions.items);
      cursor.value = versions.next_cursor;
      await refresh();
      if (alive) schedule();
    }),
);
onBeforeUnmount(() => {
  alive = false;
  clearTimeout(timer);
  emit("blocking", false);
});
</script>

<template>
  <section class="panel character-batch" aria-label="分段角色创作">
    <h2>分段生成与断点续接</h2>
    <p>
      本批 {{ draft.document.ids.length }} 人，分
      {{
        passCount
      }}
      段。每段读取前面所有已完成候选；完成后统一检查和编辑。人数不会因重试改变。
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <fieldset :disabled="busy">
      <legend>本次生成设置</legend>
      <label
        >分段创作要求<textarea v-model="notes" rows="4" maxlength="10000" />
      </label>
      <label
        >每段人数<input v-model.number="size" type="number" min="1" max="5"
      /></label>
      <label
        >每段最大输出 token<input
          v-model.number="tokens"
          type="number"
          min="64"
          max="32768"
      /></label>
      <p>
        {{
          model
            ? `${model.name} / ${model.model} · ${model.price.currency}`
            : "尚未配置角色设计模型与价格"
        }}
      </p>
      <label
        >本次整批费用上限<input
          v-model="budget"
          type="number"
          min="0.000001"
          step="0.01"
          placeholder="自行填写；保存任务不产生调用费用"
      /></label>
      <label
        >分段角色方法<select v-model="agent">
          <option value="">系统预置分段方法</option>
          <option v-for="method in agents" :key="method.id" :value="method.id">
            {{ method.definition.name }} · {{ method.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <button v-if="cursor" @click="moreAgents">加载更多方法</button>
      <div class="actions">
        <button
          :disabled="!model || !budget || !notes.trim() || passCount > 32"
          @click="prepare"
        >
          保存分段任务（不调用模型）</button
        ><button @click="reset">清除未保存的任务设置</button>
      </div>
    </fieldset>
    <section>
      <h3>本批任务历史</h3>
      <p v-if="!runs.length">尚无分段任务。</p>
      <div class="actions">
        <button
          v-for="run in runs"
          :key="run.id"
          :disabled="busy"
          @click="choose(run.id)"
        >
          {{ recipeStates[run.state] ?? run.state }} ·
          {{ run.id.slice(0, 8) }}</button
        ><button :disabled="busy" @click="perform(refresh)">刷新进度</button>
      </div>
    </section>
    <section v-if="report" aria-label="分段角色结果">
      <h3>
        {{ recipeStates[report.run.state] ?? report.run.state }} · 已完成
        {{ report.completed }} / {{ report.total }} 人
      </h3>
      <p v-if="report.stale" role="status">{{ report.stale_reason }}</p>
      <p>
        本次上限 {{ report.run.budget_limit_micros / 1e6 }}
        {{ report.run.currency }}；已计费
        {{ report.run.spent_micros / 1e6 }}，占用
        {{ report.run.held_micros / 1e6 }}。
      </p>
      <p v-if="report.run.uncertain_calls">
        存在结果未知的请求，需先对账；系统不会自动重发。
      </p>
      <div class="actions">
        <button
          v-if="report.run.state === 'PREPARED'"
          :disabled="busy || report.stale"
          @click="control('start')"
        >
          开始分段生成（将调用模型）
        </button>
        <button
          v-if="report.run.state === 'RUNNING' && !report.run.paused"
          :disabled="busy"
          @click="control('pause')"
        >
          暂停分段生成
        </button>
        <button
          v-if="report.run.paused"
          :disabled="busy || report.stale"
          @click="control('resume')"
        >
          继续分段生成
        </button>
        <button
          v-if="!['COMPLETED', 'CANCELLED'].includes(report.run.state)"
          :disabled="busy"
          @click="control('cancel')"
        >
          取消剩余任务
        </button>
      </div>
      <ol>
        <li v-for="stage in stages" :key="stage.id">
          第 {{ stage.ordinal + 1 }} 段 ·
          {{ recipeStates[stage.state] ?? stage.state }} · 尝试
          {{ stage.attempt
          }}<button
            v-if="stage.state === 'FAILED'"
            :disabled="busy || report.stale || !!report.run.uncertain_calls"
            @click="retry(stage)"
          >
            重试第 {{ stage.ordinal + 1 }} 段
          </button>
        </li>
      </ol>
      <details v-for="pass in report.passes" :key="pass.key">
        <summary>
          {{ pass.result.entities.map((e) => e.name).join("、") }}
        </summary>
        <p>{{ pass.result.explanation }}</p>
        <p v-for="warning in pass.result.warnings" :key="warning">
          {{ warning }}
        </p>
        <article v-for="person in pass.result.entities" :key="person.id">
          <h4>{{ person.name }}</h4>
          <p>{{ person.description }}</p>
        </article>
      </details>
      <button
        v-if="report.suggestion"
        :disabled="busy || dirty"
        @click="emit('suggestion', report.suggestion)"
      >
        预览整批角色候选
      </button>
    </section>
  </section>
</template>

<style scoped>
.character-batch,
fieldset {
  display: grid;
  gap: 0.75rem;
}
fieldset {
  padding: 1rem;
}
label {
  display: grid;
  gap: 0.3rem;
}
input,
textarea,
select {
  padding: 0.5rem;
  max-width: 100%;
}
.actions {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}
details {
  padding: 0.75rem;
  border: 1px solid #ddd;
  border-radius: 0.5rem;
}
li {
  margin: 0.5rem 0;
}
li button {
  margin-left: 0.75rem;
}
</style>
