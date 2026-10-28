<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
import type { NovelBook } from "./types";
import type { ChapterDraft } from "./chapterTypes";
import {
  recipeStates,
  type RecipeRun,
  type RecipeStage,
} from "../skills/recipeTypes";
import {
  editorialFindings,
  editorialPlatforms,
  editorialSources,
} from "./editorial";
const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
}>();
const emit = defineEmits<{ close: []; expired: []; edit: [id: string] }>();
type Chapter = { id: string; title: string; ready: boolean; reason: string };
type Option = {
  role: string;
  name: string;
  model: string;
  price: { id: string; currency: string };
};
type BatchStage = RecipeStage & {
  frozen: RecipeStage["frozen"] & {
    input: RecipeStage["frozen"]["input"] & {
      novel_chapter?: { resource_id: string; book_id: string };
    };
  };
};
const root = `/novels/${props.book.id}/writing-batches`;
const recipes = `/projects/${props.project.id}/recipe-executions`;
const chapters = ref<Chapter[]>([]),
  options = ref<Option[]>([]),
  runs = ref<RecipeRun[]>([]);
const selected = ref<RecipeRun | null>(null),
  start = ref(""),
  count = ref(10);
const platform = ref("general"),
  audience = ref("general"),
  focus = ref(props.book.setup.genre),
  notes = ref("");
const budget = ref(""),
  operation = ref(newOperationID());
const busy = ref(false),
  error = ref(""),
  notice = ref("");
const candidate = ref<ChapterDraft | null>(null),
  original = ref<ChapterDraft | null>(null);
const writer = computed(() => options.value.find((o) => o.role === "writer"));
watch([start, count, platform, audience, focus, notes, budget], () => {
  operation.value = newOperationID();
});
const selection = computed(() => {
  const index = chapters.value.findIndex((c) => c.id === start.value);
  return index < 0 ? [] : chapters.value.slice(index, index + count.value);
});
const canEdit = computed(() =>
  [props.actor.role, props.project.role].every((role) =>
    ["owner", "editor"].includes(role),
  ),
);
const stages = computed(
  () => (selected.value?.steps ?? []).filter((s) => s.active) as BatchStage[],
);
const allText = computed(() =>
  stages.value
    .map((s) => {
      const o = s.output as { text?: string } | undefined;
      return o?.text ?? "";
    })
    .join("\n\n"),
);
const findings = computed(() => editorialFindings(allText.value));
let disposed = false,
  timer: ReturnType<typeof setTimeout> | undefined;
let stateRequest = 0,
  stateWatermark = 0;
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
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function belongs(run: RecipeRun) {
  const input = (
    run.frozen as RecipeRun["frozen"] & { input?: { book_id?: string } }
  ).input;
  return (
    !run.retention &&
    run.frozen.definition.definition.key.startsWith("novel-batch-") &&
    input?.book_id === props.book.id
  );
}
function installSelected(run: RecipeRun) {
  selected.value = run;
  // Already in-flight snapshots have no newer accounting revision to compare.
  stateWatermark = stateRequest;
}
function updateSelected(run: RecipeRun, request: number) {
  if (
    !disposed &&
    selected.value?.id === run.id &&
    (run.revision > selected.value.revision ||
      (run.revision === selected.value.revision && request > stateWatermark))
  )
    installSelected(run);
}
async function refresh() {
  const value = await api<{ items: RecipeRun[] }>(
    `${recipes}?branch_id=${props.book.branch_id}`,
  );
  if (disposed) return;
  runs.value = value.items.filter(belongs);
  const id = selected.value?.id;
  if (id) {
    const request = ++stateRequest;
    const run = await api<RecipeRun>(`${recipes}/${id}`);
    updateSelected(run, request);
  }
}
async function prepare() {
  await perform(async () => {
    if (
      !writer.value ||
      selection.value.length !== count.value ||
      selection.value.some((c) => !c.ready)
    )
      throw new Error("请选择1—10章连续且已确认细纲的章节。");
    const micros = Math.round(Number(budget.value) * 1000000);
    if (!Number.isSafeInteger(micros) || micros < 1 || micros > 1000000000000)
      throw new Error("请填写本批总费用上限；准备请求不会发起模型调用。");
    ++stateRequest;
    const run = await api<RecipeRun>(root, "POST", {
      chapter_ids: selection.value.map((c) => c.id),
      platform: platform.value,
      audience: audience.value,
      focus: focus.value,
      notes: notes.value,
      price_id: writer.value.price.id,
      currency: writer.value.price.currency,
      budget_limit_micros: micros,
      max_output_tokens: Math.min(
        32768,
        props.book.setup.chapter_max * 2 + 2000,
      ),
      operation_id: operation.value,
    });
    if (disposed) return;
    installSelected(run);
    candidate.value = null;
    original.value = null;
    operation.value = newOperationID();
    notice.value =
      "批次已保存。核对章节、模型和费用上限后再开始；不会自动覆盖私人稿或发布。";
    await refresh();
  });
}
async function control(action: string) {
  await perform(async () => {
    if (!selected.value) return;
    const run = selected.value;
    const request = ++stateRequest;
    const value = await api<RecipeRun>(
      `${recipes}/${run.id}/${action === "start" ? "start" : "control"}`,
      "POST",
      {
        expected_revision: run.revision,
        ...(action === "start" ? {} : { action }),
      },
    );
    updateSelected(value, request);
  });
}
async function choose(id: string) {
  await perform(async () => {
    const request = ++stateRequest;
    const run = await api<RecipeRun>(`${recipes}/${id}`);
    if (!disposed) {
      if (selected.value?.id === run.id) updateSelected(run, request);
      else installSelected(run);
      candidate.value = null;
      original.value = null;
    }
  });
}
async function preview(step: BatchStage) {
  await perform(async () => {
    const resource = step.frozen.input.novel_chapter?.resource_id;
    if (!resource || selected.value?.state !== "COMPLETED") return;
    const base = `/novels/${props.book.id}/chapters/${resource}`;
    const [before, proposed] = await Promise.all([
      api<ChapterDraft>(`${base}/draft`),
      api<ChapterDraft>(`${base}/writer/${step.generation_id}`),
    ]);
    if (!disposed) {
      original.value = before;
      candidate.value = proposed;
    }
  });
}
async function adopt() {
  await perform(async () => {
    if (!candidate.value || !original.value) return;
    const value = candidate.value;
    await api(
      `/novels/${props.book.id}/chapters/${value.resource.id}/draft`,
      "PUT",
      {
        ...value,
        revision: original.value.revision,
        base_head: original.value.base_head,
      },
    );
    if (!disposed) {
      notice.value = `《${value.document.title}》已加入私人稿，可继续精修、独立记账和审稿。`;
      candidate.value = null;
      original.value = null;
    }
  });
}
async function retry(step: BatchStage) {
  await perform(async () => {
    if (!selected.value) return;
    const run = selected.value;
    const request = ++stateRequest;
    const value = await api<RecipeRun>(`${recipes}/${run.id}/retry`, "POST", {
      expected_revision: run.revision,
      operation_id: newOperationID(),
      step: step.frozen.input,
    });
    if (!disposed && selected.value?.id === run.id) {
      updateSelected(value, request);
      notice.value = "已建立新的尝试；依赖本章的后续章节也会重新生成。";
    }
  });
}
function schedule() {
  timer = setTimeout(async () => {
    if (disposed) return;
    if (
      !busy.value &&
      selected.value &&
      !["COMPLETED", "CANCELLED"].includes(selected.value.state)
    ) {
      try {
        await refresh();
      } catch (e) {
        fail(e);
      }
    }
    if (!disposed) schedule();
  }, 2500);
}
onMounted(
  () =>
    void perform(async () => {
      const [list, modelOptions] = await Promise.all([
        api<{ items: Chapter[] }>(`${root}/chapters`),
        api<{ items: Option[] }>(
          `/projects/${props.project.id}/generation-options`,
        ),
      ]);
      if (disposed) return;
      chapters.value = list.items;
      options.value = modelOptions.items;
      start.value = list.items[0]?.id ?? "";
      count.value = Math.min(10, Math.max(1, list.items.length));
      await refresh();
      if (!disposed) schedule();
    }),
);
onBeforeUnmount(() => {
  disposed = true;
  clearTimeout(timer);
});
</script>

<template>
  <section class="novel-batch-desk">
    <header class="section-heading">
      <div>
        <p class="eyebrow">作者写作台 · 快速连写</p>
        <h1>{{ book.setup.title }}</h1>
      </div>
      <button :disabled="busy" @click="emit('close')">返回小说</button>
    </header>
    <p>
      选择连续1—10章，按已确认细纲逐章生成。每章衔接前章候选和累计状态；关闭页面后可从批次记录继续查看。
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <form class="panel batch-form" @submit.prevent="prepare">
      <h2>准备本批创作</h2>
      <label
        >起始章节<select v-model="start" aria-label="起始章节" :disabled="busy">
          <option
            v-for="(chapter, i) in chapters"
            :key="chapter.id"
            :value="chapter.id"
          >
            {{ i + 1 }}. {{ chapter.title
            }}{{ chapter.ready ? "" : "（细纲待确认）" }}
          </option>
        </select></label
      >
      <label
        >连续章节数<input
          v-model.number="count"
          type="number"
          min="1"
          max="10"
          required
          :disabled="busy"
      /></label>
      <label
        >目标平台<select
          v-model="platform"
          aria-label="目标平台"
          :disabled="busy"
        >
          <option v-for="p in editorialPlatforms" :key="p.id" :value="p.id">
            {{ p.name }}
          </option>
        </select></label
      >
      <label
        >目标读者<select
          v-model="audience"
          aria-label="目标读者"
          :disabled="busy"
        >
          <option value="general">不限</option>
          <option value="male">男频侧重</option>
          <option value="female">女频侧重</option>
        </select></label
      >
      <label
        >题材与编辑重点<input v-model="focus" maxlength="1000" :disabled="busy"
      /></label>
      <label
        >本批要求<textarea
          v-model="notes"
          maxlength="6000"
          rows="3"
          :disabled="busy"
          placeholder="保留哪些高光、希望怎样的节奏、哪些句子不要改"
        />
      </label>
      <p>
        写作模型：{{
          writer
            ? `${writer.name} · ${writer.model}`
            : "请先在后台配置写作模型与价格"
        }}
      </p>
      <label
        >本批总费用上限（{{
          writer?.price.currency ?? "按模型价格币种"
        }}）<input
          v-model="budget"
          aria-label="本批总费用上限"
          type="number"
          min="0.000001"
          step="0.000001"
          required
          :disabled="busy"
      /></label>
      <p class="muted">
        只准备不调用模型；开始后沿用后台付费开关和总额度。候选状态未进入正史，完整发布仍需独立记账和审稿。
      </p>
      <button
        :disabled="
          busy ||
          !canEdit ||
          !writer ||
          selection.length !== count ||
          selection.some((c) => !c.ready)
        "
      >
        准备 {{ selection.length }} 章
      </button>
    </form>
    <section class="panel">
      <h2>已保存批次</h2>
      <button :disabled="busy" @click="perform(refresh)">刷新记录</button>
      <p v-if="!runs.length">暂无批次。</p>
      <div class="batch-runs">
        <button
          v-for="run in runs"
          :key="run.id"
          :disabled="busy"
          @click="choose(run.id)"
        >
          {{ run.frozen.definition.definition.steps?.length }}章 ·
          {{ recipeStates[run.state] ?? run.state }} · {{ run.id.slice(-6) }}
        </button>
      </div>
    </section>
    <section v-if="selected" class="panel" aria-label="连续章节进度">
      <h2>
        {{ recipeStates[selected.state] ?? selected.state
        }}{{ selected.paused ? " · 已暂停" : "" }}
      </h2>
      <p>
        已完成 {{ stages.filter((s) => s.state === "COMPLETED").length }}/{{
          stages.length
        }}
        章；已记费用 {{ selected.spent_micros / 1000000 }} / 上限
        {{ selected.budget_limit_micros / 1000000 }} {{ selected.currency }}
      </p>
      <p v-if="selected.reason">
        任务尚有待处理的问题，请核对写作依据、模型连接和费用条件。失败章节可在条件恢复后重试；结果未知时先核对费用记录。
      </p>
      <div class="batch-runs">
        <button
          v-if="selected.state === 'PREPARED'"
          :disabled="busy || !canEdit"
          @click="control('start')"
        >
          确认开始本批</button
        ><template
          v-if="
            !['COMPLETED', 'CANCELLED', 'PREPARED'].includes(selected.state)
          "
          ><button
            :disabled="busy || !canEdit"
            @click="
              control(
                selected.paused || selected.state === 'BLOCKED'
                  ? 'resume'
                  : 'pause',
              )
            "
          >
            {{
              selected.paused || selected.state === "BLOCKED"
                ? "恢复任务"
                : "暂停任务"
            }}</button
          ><button :disabled="busy || !canEdit" @click="control('cancel')">
            取消未完成部分
          </button></template
        >
      </div>
      <ol>
        <li v-for="step in stages" :key="step.id">
          <strong>第{{ step.ordinal + 1 }}章</strong> ·
          {{ recipeStates[step.state] ?? step.state
          }}<button
            v-if="selected.state === 'COMPLETED'"
            :disabled="busy || !canEdit"
            @click="preview(step)"
          >
            对照与采用</button
          ><button
            v-if="step.state === 'FAILED' && !selected.uncertain_calls"
            :disabled="busy || !canEdit"
            @click="retry(step)"
          >
            重试本章及后续</button
          ><button
            v-if="step.frozen.input.novel_chapter"
            :disabled="busy"
            @click="emit('edit', step.frozen.input.novel_chapter.resource_id)"
          >
            打开私人稿精修
          </button>
        </li>
      </ol>
      <details v-if="allText">
        <summary>本批文字自检 · {{ findings.length }}条提示</summary>
        <p>
          跨章重复检查为启发式提示，需结合剧情判断；没有提示不等于质量通过。
        </p>
        <ul>
          <li v-for="(finding, i) in findings" :key="i">
            <strong>{{ finding.label }}</strong>
            <blockquote>{{ finding.evidence }}</blockquote>
            {{ finding.advice }}
          </li>
        </ul>
      </details>
    </section>
    <section
      v-if="candidate && original"
      class="panel"
      aria-label="批次章节候选对照"
    >
      <h2>{{ candidate.document.title }}</h2>
      <div class="batch-compare">
        <article>
          <h3>当前私人稿</h3>
          <pre>{{ original.document.text || "尚无正文" }}</pre>
        </article>
        <article>
          <h3>本批候选</h3>
          <pre>{{ candidate.document.text }}</pre>
        </article>
      </div>
      <details>
        <summary>候选状态与待核对事项</summary>
        <ul>
          <li v-for="(note, i) in candidate.notes" :key="i">{{ note }}</li>
        </ul>
      </details>
      <button :disabled="busy || !canEdit" @click="adopt">
        确认加入本章私人稿</button
      ><button
        :disabled="busy"
        @click="
          candidate = null;
          original = null;
        "
      >
        暂不采用
      </button>
    </section>
    <section class="panel">
      <h2>投稿前检查</h2>
      <p>
        平台正式要求和编辑建议分开使用。目标读者影响侧重，具体题材优先；连写数量、改写比例或某个AI分数都不能保证人工审核通过。
      </p>
      <ul>
        <li v-for="source in editorialSources" :key="source.url">
          <a :href="source.url" target="_blank" rel="noopener noreferrer">{{
            source.name
          }}</a>
        </li>
      </ul>
    </section>
  </section>
</template>
<style scoped>
.novel-batch-desk {
  max-width: 1100px;
  margin: 24px auto;
  padding: 0 20px 32px;
}
.section-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
}
.panel {
  margin-top: 20px;
}
.batch-form {
  display: grid;
  gap: 12px;
}
label {
  display: grid;
  gap: 6px;
}
.batch-runs {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 12px 0;
}
li {
  margin: 12px 0;
}
li button {
  margin-left: 8px;
}
.batch-compare {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
pre {
  white-space: pre-wrap;
  max-height: 60vh;
  overflow: auto;
}
blockquote {
  white-space: pre-wrap;
}
@media (max-width: 750px) {
  .batch-compare {
    grid-template-columns: 1fr;
  }
}
</style>
