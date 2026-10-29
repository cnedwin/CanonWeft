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
import { useBoundedReads } from "../../composables/useBoundedReads";
import { useManuscriptReads } from "./useManuscriptReads";
import NovelReviewIssues from "./NovelReviewIssues.vue";
import {
  bookReviewCategories,
  type BookReviewEditTarget,
  type BookReviewFilters,
  type BookReviewPreview as Preview,
  type BookReviewReport as Report,
  type BookReviewViewState,
} from "./bookReviewTypes";
const props = defineProps<{
  book: NovelBook;
  project: Project;
  initialRunId?: string;
  initialFilters?: Partial<BookReviewFilters>;
  initialFocusedIssue?: string;
}>();
const emit = defineEmits<{
  close: [];
  expired: [];
  edit: [target: BookReviewEditTarget];
  view: [state: BookReviewViewState];
}>();
type ModelOption = {
  role: string;
  name: string;
  model: string;
  price: { id: string; currency: string };
};
const root = `/novels/${props.book.id}/book-reviews`;
const recipes = `/projects/${props.project.id}/recipe-executions`;
const { read, active } = useBoundedReads({
  stopped: "审稿记录读取已停止。",
  timeout: "审稿记录读取超时，输入仍保留，可以重新读取。",
});
const manuscriptReads = useManuscriptReads(props.book.id);
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
const operation = ref(newOperationID()),
  agentCursor = ref("");
const selectedRun = ref(props.initialRunId ?? ""),
  listLoading = ref(false),
  reportLoading = ref(false),
  setupLoading = ref(false),
  issueBlocking = ref(false);
const listError = ref(""),
  reportError = ref(""),
  setupError = ref("");
const prepareRegion = ref<HTMLElement | null>(null);
const savedView = ref<BookReviewViewState>({
  runId: props.initialRunId ?? "",
  filters: {
    query: "",
    severity: "",
    category: "",
    chapterId: "",
    disposition: "",
    ...props.initialFilters,
  },
  focusedIssue: props.initialFocusedIssue ?? "",
});
const critic = computed(() =>
  models.value.find((option) => option.role === "critic"),
);
const stages = computed(() =>
  (report.value?.run.steps ?? []).filter((stage) => stage.active),
);
const needsConsent = computed(
  () =>
    !!preview.value &&
    (preview.value.missing > 0 || preview.value.warnings > 0),
);
const labels = bookReviewCategories;
const checks: Record<string, string> = {
  no_issue_found: "本段未发现问题",
  issues_found: "发现待修订问题",
  insufficient: "依据不足",
};
let alive = true,
  listEpoch = 0,
  reportEpoch = 0,
  setupEpoch = 0,
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
function reviewAgents(items: CapabilityVersion[]) {
  return items.filter(
    (value) =>
      value.definition.kind === "agent" &&
      value.definition.role === "critic" &&
      Object.hasOwn(
        (value.definition.output_schema.properties ?? {}) as object,
        "coverage",
      ),
  );
}
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
function denied(status: number) {
  ++reportEpoch;
  report.value = null;
  issueBlocking.value = false;
  reportError.value =
    status === 401
      ? "登录已失效，请重新登录后读取报告。"
      : "当前权限不能继续读取本报告与处理详情。";
}
function remember(value: BookReviewViewState) {
  savedView.value = value;
  emit("view", value);
}
async function perform(action: () => Promise<void>) {
  if (busy.value || issueBlocking.value) return;
  busy.value = true;
  error.value = "";
  ++reportEpoch;
  ++listEpoch;
  reportLoading.value = false;
  listLoading.value = false;
  try {
    await action();
  } catch (e) {
    if (alive) fail(e);
  } finally {
    if (alive) busy.value = false;
  }
}
async function loadRuns() {
  const request = ++listEpoch,
    currentReport = reportEpoch;
  listLoading.value = true;
  listError.value = "";
  try {
    const value = await read<{ items: RecipeRun[] }>(root);
    if (alive && request === listEpoch) runs.value = value.items;
  } catch (e) {
    if (!alive || request !== listEpoch) return;
    if (e instanceof APIError && [401, 403].includes(e.status)) {
      runs.value = [];
      if (currentReport === reportEpoch) denied(e.status);
      if (e.status === 401) emit("expired");
    }
    listError.value = message(e);
  } finally {
    if (alive && request === listEpoch) listLoading.value = false;
  }
}
async function loadReport(id: string) {
  if (!id) return;
  const request = ++reportEpoch;
  reportLoading.value = true;
  reportError.value = "";
  try {
    const value = await read<Report>(`${root}/${encodeURIComponent(id)}`);
    if (
      !alive ||
      !active() ||
      request !== reportEpoch ||
      selectedRun.value !== id
    )
      return;
    if (value.run.id !== id)
      throw new Error("读取报告与所选任务不一致，请重新读取。");
    report.value = value;
  } catch (e) {
    if (!alive || request !== reportEpoch || selectedRun.value !== id) return;
    if (e instanceof APIError && [401, 403].includes(e.status)) {
      report.value = null;
      issueBlocking.value = false;
      if (e.status === 401) emit("expired");
    }
    reportError.value = message(e);
  } finally {
    if (alive && request === reportEpoch) reportLoading.value = false;
  }
}
async function reload() {
  await Promise.all([loadRuns(), loadReport(selectedRun.value)]);
}
async function choose(id: string) {
  if (busy.value || issueBlocking.value) return;
  if (selectedRun.value !== id) {
    selectedRun.value = id;
    report.value = null;
    issueBlocking.value = false;
    savedView.value = {
      runId: id,
      filters: {
        query: "",
        severity: "",
        category: "",
        chapterId: "",
        disposition: "",
      },
      focusedIssue: "",
    };
  }
  await loadReport(id);
}
async function loadSetup() {
  const request = ++setupEpoch;
  setupLoading.value = true;
  setupError.value = "";
  const failures: string[] = [];
  const valid = () => alive && request === setupEpoch;
  await Promise.allSettled([
    (async () => {
      try {
        const value = await read<{ items: ModelOption[] }>(
          `/projects/${props.project.id}/generation-options`,
        );
        if (valid()) models.value = value.items;
      } catch (e) {
        if (!valid()) return;
        if (e instanceof APIError && [401, 403].includes(e.status)) {
          models.value = [];
          if (e.status === 401) emit("expired");
        }
        failures.push("模型配置：" + message(e));
      }
    })(),
    (async () => {
      try {
        const value = await read<{
          items: CapabilityVersion[];
          next_cursor: string;
        }>(`/projects/${props.project.id}/capabilities`);
        if (valid()) {
          agents.value = reviewAgents(value.items);
          agentCursor.value = value.next_cursor;
        }
      } catch (e) {
        if (!valid()) return;
        if (e instanceof APIError && [401, 403].includes(e.status)) {
          agents.value = [];
          agentCursor.value = "";
          if (e.status === 401) emit("expired");
        }
        failures.push("审稿方法：" + message(e));
      }
    })(),
    (async () => {
      try {
        const value = await manuscriptReads.preview({
          source: "draft",
          author: "审稿",
          language: "zh-Hans",
        });
        if (valid())
          catalog.value = [
            ...new Map(
              value.chapters.map((chapter) => [
                chapter.volume_id,
                { id: chapter.volume_id, title: chapter.volume },
              ]),
            ).values(),
          ];
      } catch (e) {
        if (!valid()) return;
        if (e instanceof APIError && [401, 403].includes(e.status)) {
          catalog.value = [];
          if (e.status === 401) emit("expired");
        }
        failures.push("分卷目录：" + message(e));
      }
    })(),
  ]);
  if (valid()) {
    setupError.value = failures.join("；");
    setupLoading.value = false;
  }
}
async function moreAgents() {
  if (busy.value || setupLoading.value || !agentCursor.value) return;
  setupLoading.value = true;
  setupError.value = "";
  try {
    const value = await read<{
      items: CapabilityVersion[];
      next_cursor: string;
    }>(
      `/projects/${props.project.id}/capabilities?after=${encodeURIComponent(agentCursor.value)}`,
    );
    if (alive) {
      agents.value.push(...reviewAgents(value.items));
      agentCursor.value = value.next_cursor;
    }
  } catch (e) {
    if (alive) {
      setupError.value = message(e);
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        agents.value = [];
        agentCursor.value = "";
        if (e.status === 401) emit("expired");
      }
    }
  } finally {
    if (alive) setupLoading.value = false;
  }
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
    if (!alive) return;
    selectedRun.value = value.id;
    report.value = null;
    operation.value = newOperationID();
    savedView.value = {
      runId: value.id,
      filters: {
        query: "",
        severity: "",
        category: "",
        chapterId: "",
        disposition: "",
      },
      focusedIssue: "",
    };
    notice.value =
      "审稿任务已保存。准备阶段不调用模型；核对范围、模型和费用后再开始。";
    await reload();
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
  return (
    report.value?.preview.chapters.find((chapter) => chapter.id === id)
      ?.title ?? id
  );
}
function prepareAgain() {
  if (!report.value || busy.value || issueBlocking.value) return;
  const frozen = (report.value.run.frozen as unknown as { input?: unknown })
    .input;
  let input = frozen;
  if (typeof input === "string") {
    try {
      input = JSON.parse(input);
    } catch {
      input = undefined;
    }
  }
  const request =
    input && typeof input === "object"
      ? (input as { request?: { volume_id?: unknown; notes?: unknown } })
          .request
      : undefined;
  source.value = "draft";
  volume.value =
    typeof request?.volume_id === "string" ? request.volume_id : "";
  if (typeof request?.notes === "string")
    notes.value = request.notes.slice(0, 6000);
  preview.value = null;
  incomplete.value = false;
  notice.value =
    "已将原审稿范围和重点填回准备区，正文来源改为当前已保存稿。请重新检查范围并明确保存新任务；尚未准备或启动任何模型调用。";
  prepareRegion.value?.scrollIntoView({ block: "start" });
}
function schedule() {
  timer = setTimeout(async () => {
    if (!alive) return;
    if (
      !busy.value &&
      !issueBlocking.value &&
      report.value?.run.state === "RUNNING" &&
      !report.value.run.paused
    )
      await reload();
    if (alive) schedule();
  }, 3000);
}
onMounted(() => {
  void loadSetup();
  void reload();
  schedule();
});
onBeforeUnmount(() => {
  alive = false;
  ++listEpoch;
  ++reportEpoch;
  ++setupEpoch;
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
      <button :disabled="busy || issueBlocking" @click="emit('close')">
        返回小说工作台
      </button>
    </header>
    <p>
      按顺序阅读全文，检查人物、因果、伏笔、主题、高光和语言。问题保留原文引用，修订建议由你决定是否采用。
    </p>
    <fieldset ref="prepareRegion" :disabled="busy || issueBlocking">
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
          <option
            v-if="volume && !catalog.some((item) => item.id === volume)"
            :value="volume"
          >
            原报告分卷 {{ volume.slice(0, 8) }}（请重新核对目录）
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
      <button v-if="agentCursor" :disabled="setupLoading" @click="moreAgents">
        加载更多已保存方法
      </button>
      <p v-if="setupError" role="alert">
        准备区配置：{{ setupError }}。已有审稿报告仍可阅读。
      </p>
      <button :disabled="setupLoading" @click="loadSetup">
        刷新准备区配置
      </button>
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
      <button
        :disabled="busy || issueBlocking || listLoading || reportLoading"
        @click="reload"
      >
        刷新审稿记录
      </button>
    </div>
    <p v-if="listLoading || reportLoading" role="status">正在读取审稿记录…</p>
    <p v-if="listError" role="alert">{{ listError }}</p>
    <p v-if="reportError" role="alert">{{ reportError }}</p>
    <p v-if="!runs.length && !listLoading && !listError">还没有审稿任务。</p>
    <ul>
      <li v-for="r in runs" :key="r.id">
        <button :disabled="busy || issueBlocking" @click="choose(r.id)">
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
        <button :disabled="busy || issueBlocking" @click="prepareAgain">
          以当前稿准备重新审稿
        </button>
        <button
          v-if="report.run.state === 'PREPARED'"
          :disabled="
            busy ||
            issueBlocking ||
            report.stale ||
            report.run.restored_disabled
          "
          @click="control('start')"
        >
          开始审稿（将调用模型）
        </button>
        <button
          v-if="report.run.state === 'RUNNING' && !report.run.paused"
          :disabled="busy || issueBlocking"
          @click="control('pause')"
        >
          暂停审稿
        </button>
        <button
          v-if="report.run.paused"
          :disabled="busy || issueBlocking || report.stale"
          @click="control('resume')"
        >
          继续审稿
        </button>
        <button
          v-if="!['COMPLETED', 'CANCELLED'].includes(report.run.state)"
          :disabled="busy || issueBlocking"
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
              issueBlocking ||
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
      <NovelReviewIssues
        :key="report.run.id"
        :root="root"
        :report="report"
        :disabled="busy"
        :initial-filters="savedView.filters"
        :initial-focused-issue="savedView.focusedIssue"
        @blocking="issueBlocking = $event"
        @view="remember"
        @edit="emit('edit', $event)"
        @expired="emit('expired')"
        @denied="denied"
      />
      <section
        v-for="p in report.passes"
        :key="p.step_id ?? p.key"
        class="review-pass"
      >
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
        <p v-else>
          本段列出
          {{ p.result.findings.length }} 项问题，可在上方问题工作台筛选和处理。
        </p>
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
