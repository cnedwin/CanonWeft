<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Project,
} from "../../api";
import type { NovelBook } from "./types";
import {
  recipeStates,
  type RecipeRun,
  type RecipeStage,
} from "../skills/recipeTypes";
import type { CapabilityVersion } from "../skills/capabilityTypes";
const props = defineProps<{ book: NovelBook; project: Project }>();
const emit = defineEmits<{ close: []; expired: []; edit: [id: string] }>();
type Chapter = {
  id: string;
  title: string;
  volume_id: string;
  volume: string;
  included: boolean;
  warning: string;
};
type Preview = {
  hash: string;
  included: number;
  missing: number;
  warnings: number;
  chapters: Chapter[];
  passes: number;
};
type Evidence = { chapter_id: string; quote: string };
type Finding = {
  id: string;
  category: string;
  severity: string;
  title: string;
  explanation: string;
  suggestion: string;
  evidence: Evidence[];
};
type Pass = {
  key: string;
  chapter_ids: string[];
  result: {
    coverage: string[];
    checks: Record<string, string>;
    findings: Finding[];
    carry: {
      key: string;
      category: string;
      summary: string;
      status: string;
      evidence: Evidence[];
    }[];
    limits: string[];
  };
};
type Report = {
  run: RecipeRun;
  preview: Preview;
  stale: boolean;
  stale_reason: string;
  passes: Pass[];
  covered: number;
  limits: string[];
};
type ModelOption = {
  role: string;
  name: string;
  model: string;
  price: { id: string; currency: string };
};
const root = `/novels/${props.book.id}/book-reviews`;
const recipes = `/projects/${props.project.id}/recipe-executions`;
const source = ref("draft"),
  volume = ref(""),
  notes = ref(""),
  budget = ref(""),
  agent = ref("");
const tokens = ref(8192),
  incomplete = ref(false),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const preview = ref<Preview | null>(null),
  report = ref<Report | null>(null),
  runs = ref<RecipeRun[]>([]);
const catalog = ref<{ id: string; title: string }[]>([]),
  models = ref<ModelOption[]>([]),
  agents = ref<CapabilityVersion[]>([]);
const operation = ref(newOperationID());
const agentCursor = ref("");
function reviewAgents(items: CapabilityVersion[]) {
  return items.filter(
    (v) =>
      v.definition.kind === "agent" &&
      v.definition.role === "critic" &&
      Object.hasOwn(
        (v.definition.output_schema.properties ?? {}) as object,
        "coverage",
      ),
  );
}
async function moreAgents() {
  await perform(async () => {
    const page = await api<{ items: CapabilityVersion[]; next_cursor: string }>(
      `/projects/${props.project.id}/capabilities?after=${encodeURIComponent(agentCursor.value)}`,
    );
    if (alive) {
      agents.value.push(...reviewAgents(page.items));
      agentCursor.value = page.next_cursor;
    }
  });
}
const critic = computed(() => models.value.find((o) => o.role === "critic"));
const stages = computed(() =>
  (report.value?.run.steps ?? []).filter((s) => s.active),
);
const needsConsent = computed(
  () =>
    !!preview.value &&
    (preview.value.missing > 0 || preview.value.warnings > 0),
);
const labels: Record<string, string> = {
  causality: "因果与时空",
  character: "人物发展",
  foreshadow: "伏笔与兑现",
  theme: "主线与主题",
  highlight: "人物高光",
  language: "语言与细节",
};
const checks: Record<string, string> = {
  no_issue_found: "本段未发现问题",
  issues_found: "发现待修订问题",
  insufficient: "依据不足",
};
const severities: Record<string, string> = {
  major: "优先修订",
  minor: "局部优化",
  question: "待作者确认",
};
let alive = true,
  timer: ReturnType<typeof setTimeout> | undefined;
watch(
  [source, volume],
  () => {
    preview.value = null;
    incomplete.value = false;
  },
  { flush: "sync" },
);
watch(
  [source, volume, notes, budget, agent, tokens, incomplete],
  () => {
    operation.value = newOperationID();
  },
  { flush: "sync" },
);
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
async function reload() {
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
async function inspect() {
  await perform(async () => {
    const value = await api<Preview>(`${root}/preview`, "POST", {
      source: source.value,
      volume_id: volume.value,
    });
    if (alive) {
      preview.value = value;
      incomplete.value = false;
    }
  });
}
async function prepare() {
  await perform(async () => {
    if (!preview.value || !critic.value)
      throw new Error("请先检查正文范围，并在模型设置中配置审稿模型。");
    const micros = Math.round(Number(budget.value) * 1000000);
    if (!Number.isSafeInteger(micros) || micros < 1 || micros > 1000000000000)
      throw new Error("请填写本次总费用上限。");
    const value = await api<RecipeRun>(root, "POST", {
      source: source.value,
      volume_id: volume.value,
      preview_hash: preview.value.hash,
      allow_incomplete: incomplete.value,
      notes: notes.value,
      agent_version_id: agent.value,
      price_id: critic.value.price.id,
      currency: critic.value.price.currency,
      budget_limit_micros: micros,
      max_output_tokens: tokens.value,
      operation_id: operation.value,
    });
    const result = await api<Report>(`${root}/${value.id}`);
    if (!alive) return;
    report.value = result;
    operation.value = newOperationID();
    notice.value =
      "审稿任务已保存。准备阶段不调用模型；核对范围、模型和费用后再开始。";
    await reload();
  });
}
async function control(action: string) {
  await perform(async () => {
    if (!report.value) return;
    const r = report.value.run;
    await api(
      `${recipes}/${r.id}/${action === "start" ? "start" : "control"}`,
      "POST",
      {
        expected_revision: r.revision,
        ...(action === "start" ? {} : { action }),
      },
    );
    await reload();
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
      "已建立新尝试，后续依赖段落会重新审稿；原报告仍保留在任务历史中。";
    await reload();
  });
}
function title(id: string) {
  return report.value?.preview.chapters.find((c) => c.id === id)?.title ?? id;
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
        await reload();
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
      const [modelOptions, capabilities, exportPreview] = await Promise.all([
        api<{ items: ModelOption[] }>(
          `/projects/${props.project.id}/generation-options`,
        ),
        api<{ items: CapabilityVersion[]; next_cursor: string }>(
          `/projects/${props.project.id}/capabilities`,
        ),
        api<Preview>(`/novels/${props.book.id}/export/preview`, "POST", {
          source: "draft",
          volume_id: "",
          author: "审稿",
          language: "zh-Hans",
        }),
      ]);
      if (!alive) return;
      models.value = modelOptions.items;
      agents.value = reviewAgents(capabilities.items);
      agentCursor.value = capabilities.next_cursor;
      catalog.value = [
        ...new Map(
          exportPreview.chapters.map((c) => [
            c.volume_id,
            { id: c.volume_id, title: c.volume },
          ]),
        ).values(),
      ];
      await reload();
      if (alive) schedule();
    }),
);
onBeforeUnmount(() => {
  alive = false;
  clearTimeout(timer);
});
</script>

<template>
  <section class="book-review" aria-label="整卷与全书审稿">
    <header class="section-heading">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>整卷与全书审稿</h1>
      </div>
      <button :disabled="busy" @click="emit('close')">返回小说工作台</button>
    </header>
    <p>
      按顺序阅读全文，检查人物、因果、伏笔、主题、高光和语言。问题保留原文引用，修订建议由你决定是否采用。
    </p>
    <fieldset :disabled="busy">
      <legend>准备审稿</legend>
      <label
        >正文来源<select v-model="source" aria-label="审稿正文来源">
          <option value="draft">我的已保存稿（无私人稿时使用正式稿）</option>
          <option value="published">仅正式采用稿</option>
        </select></label
      >
      <label
        >审稿范围<select v-model="volume" aria-label="审稿范围">
          <option value="">整书</option>
          <option v-for="v in catalog" :key="v.id" :value="v.id">
            {{ v.title }}
          </option>
        </select></label
      >
      <label
        >本次重点<textarea
          v-model="notes"
          aria-label="审稿重点"
          maxlength="6000"
          placeholder="例如：主角的成长是否可信，前卷伏笔是否被遗忘"
        />
      </label>
      <label
        >审稿方法<select v-model="agent" aria-label="审稿方法">
          <option value="">预置整卷审稿方法</option>
          <option v-for="a in agents" :key="a.id" :value="a.id">
            {{ a.definition.name }} · {{ a.created_at }}
          </option>
        </select></label
      >
      <button v-if="agentCursor" @click="moreAgents">加载更多已保存方法</button>
      <p class="muted">
        预置 Agent 和 Skill
        会随准备任务保存到能力库，可在那里另存编辑版本后重新选择。编辑方法仍须保留证据和覆盖范围契约。
      </p>
      <p>
        审稿模型：{{
          critic
            ? `${critic.name} / ${critic.model}`
            : "尚未配置，请先到模型设置配置独立审阅用途"
        }}
      </p>
      <label
        >本次总费用上限（{{ critic?.price.currency ?? "模型计价币种" }}）<input
          v-model="budget"
          aria-label="审稿费用上限"
          inputmode="decimal"
      /></label>
      <label
        >每段报告长度上限<input
          v-model.number="tokens"
          aria-label="审稿输出上限"
          type="number"
          min="256"
          max="32768"
          step="256"
      /></label>
      <button @click="inspect">检查审稿范围</button>
      <div v-if="preview" class="panel">
        <p>
          将读取 {{ preview.included }} 章，分 {{ preview.passes }} 段；缺少
          {{ preview.missing }} 章，{{ preview.warnings }} 项依据待核对。
        </p>
        <ul v-if="preview.warnings">
          <li
            v-for="c in preview.chapters.filter((c) => c.warning)"
            :key="c.id"
          >
            {{ c.title }}：{{ c.warning }}
          </li>
        </ul>
        <label v-if="needsConsent" class="consent"
          ><input
            v-model="incomplete"
            type="checkbox"
          />我确认按当前不完整稿审读，并保留缺章及过期依据提示</label
        >
        <button
          :disabled="!critic || (needsConsent && !incomplete)"
          @click="prepare"
        >
          保存审稿任务（不调用模型）
        </button>
      </div>
    </fieldset>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <div class="section-heading">
      <h2>审稿记录</h2>
      <button :disabled="busy" @click="perform(reload)">刷新审稿记录</button>
    </div>
    <p v-if="!runs.length">还没有审稿任务。</p>
    <ul>
      <li v-for="r in runs" :key="r.id">
        <button :disabled="busy" @click="choose(r.id)">
          {{ recipeStates[r.state] ?? r.state }} · {{ r.id.slice(0, 8) }} ·
          {{ r.spent_micros / 1000000 }} {{ r.currency }}
        </button>
      </li>
    </ul>
    <article v-if="report" class="panel" aria-label="审稿报告">
      <h2>
        {{ recipeStates[report.run.state] ?? report.run.state
        }}{{ report.run.paused ? " · 已暂停" : "" }}
      </h2>
      <p v-if="report.stale" role="status">
        报告已过期：{{ report.stale_reason }}
      </p>
      <p>
        已核对报告覆盖 {{ report.covered }} /
        {{ report.preview.included }} 章；目录另有
        {{ report.preview.missing }}
        章缺失。任务完成只表示流程执行结束，不代表作品没有问题或通过平台审核。
      </p>
      <p>
        费用 {{ report.run.spent_micros / 1000000 }} /
        {{ report.run.budget_limit_micros / 1000000 }}
        {{ report.run.currency }}；待结算
        {{ report.run.held_micros / 1000000 }}，结果未知调用
        {{ report.run.uncertain_calls }} 次。
      </p>
      <p v-if="report.run.reason">当前等待原因：{{ report.run.reason }}</p>
      <div class="actions">
        <button
          v-if="report.run.state === 'PREPARED'"
          :disabled="busy || report.stale || report.run.restored_disabled"
          @click="control('start')"
        >
          开始审稿（将调用模型）
        </button>
        <button
          v-if="report.run.state === 'RUNNING' && !report.run.paused"
          :disabled="busy"
          @click="control('pause')"
        >
          暂停审稿
        </button>
        <button
          v-if="report.run.paused"
          :disabled="busy || report.stale"
          @click="control('resume')"
        >
          继续审稿
        </button>
        <button
          v-if="!['COMPLETED', 'CANCELLED'].includes(report.run.state)"
          :disabled="busy"
          @click="control('cancel')"
        >
          取消任务
        </button>
      </div>
      <ul>
        <li v-for="s in stages" :key="s.id">
          第 {{ s.ordinal + 1 }} 段：{{ recipeStates[s.state] ?? s.state }} ·
          {{ s.frozen.config.name }} / {{ s.frozen.config.model }}
          <button
            v-if="
              ['FAILED', 'BLOCKED'].includes(report.run.state) &&
              s.state !== 'COMPLETED'
            "
            :disabled="
              busy ||
              report.stale ||
              report.run.restored_disabled ||
              report.run.uncertain_calls > 0
            "
            @click="retry(s)"
          >
            重试此段及后续段落
          </button>
        </li>
      </ul>
      <p v-for="limit in report.limits" :key="limit">{{ limit }}</p>
      <section v-for="p in report.passes" :key="p.key" class="review-pass">
        <h3>{{ p.chapter_ids.map(title).join(" / ") }}</h3>
        <dl class="checks">
          <template
            v-for="(status, category) in p.result.checks"
            :key="category"
            ><dt>{{ labels[category] ?? category }}</dt>
            <dd>{{ checks[status] ?? status }}</dd></template
          >
        </dl>
        <p v-if="!p.result.findings.length">
          本段没有列出问题，请结合依据不足项与作者判断阅读。
        </p>
        <article v-for="f in p.result.findings" :key="f.id" class="finding">
          <h4>{{ severities[f.severity] }} · {{ f.title }}</h4>
          <p>{{ f.explanation }}</p>
          <p>建议：{{ f.suggestion }}</p>
          <blockquote v-for="(e, i) in f.evidence" :key="i">
            <p>{{ e.quote }}</p>
            <button :disabled="busy" @click="emit('edit', e.chapter_id)">
              打开《{{ title(e.chapter_id) }}》编辑
            </button>
          </blockquote>
        </article>
        <details>
          <summary>向后段传递的状态（{{ p.result.carry.length }} 项）</summary>
          <ul>
            <li v-for="c in p.result.carry" :key="c.key">
              {{
                { open: "待核对", resolved: "已解释", uncertain: "不确定" }[
                  c.status
                ] ?? c.status
              }}：{{ c.summary }}
              <blockquote v-for="(e, i) in c.evidence" :key="i">
                《{{ title(e.chapter_id) }}》：{{ e.quote }}
              </blockquote>
            </li>
          </ul>
        </details>
        <ul v-if="p.result.limits.length">
          <li v-for="limit in p.result.limits" :key="limit">
            判断范围：{{ limit }}
          </li>
        </ul>
      </section>
    </article>
  </section>
</template>

<style scoped>
.book-review {
  max-width: 1100px;
  margin: auto;
}
.book-review fieldset {
  display: grid;
  gap: 1rem;
}
.book-review label {
  display: grid;
  gap: 0.35rem;
}
.book-review .consent {
  display: flex;
  align-items: center;
}
.consent input {
  width: auto;
}
.checks {
  display: grid;
  grid-template-columns: minmax(6rem, 1fr) 2fr;
  gap: 0.4rem;
}
.checks dd {
  margin: 0;
}
.review-pass {
  margin-top: 1.5rem;
  border-top: 1px solid var(--border, #ddd);
  padding-top: 1rem;
}
.finding {
  padding: 1rem;
  background: var(--surface, #fafafa);
  margin: 0.8rem 0;
}
.finding blockquote {
  border-left: 3px solid #8a9e80;
  padding-left: 1rem;
  white-space: pre-wrap;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
button {
  white-space: normal;
}
</style>
