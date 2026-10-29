<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import {
  bookReviewCategories,
  bookReviewIssueKey,
  bookReviewSeverities,
  bookReviewStatuses,
  type BookReviewChapterPin,
  type BookReviewDispositions,
  type BookReviewDispositionStatus,
  type BookReviewEditTarget,
  type BookReviewFilters,
  type BookReviewReport,
  type BookReviewViewState,
} from "./bookReviewTypes";

const props = defineProps<{
  root: string;
  report: BookReviewReport;
  disabled: boolean;
  initialFilters?: Partial<BookReviewFilters>;
  initialFocusedIssue?: string;
}>();
const emit = defineEmits<{
  expired: [];
  denied: [status: number];
  edit: [target: BookReviewEditTarget];
  view: [state: BookReviewViewState];
  blocking: [value: boolean];
}>();
const filters = ref<BookReviewFilters>({
  query: "",
  severity: "",
  category: "",
  chapterId: "",
  disposition: "",
  ...props.initialFilters,
});
const focused = ref(props.initialFocusedIssue ?? "");
const { read, active } = useBoundedReads({
  stopped: "问题处理记录读取已停止。",
  timeout: "问题处理记录读取超时，备注仍保留，可重新读取。",
});
const records = ref<BookReviewDispositions | null>(null),
  loading = ref(false),
  writing = ref(false),
  error = ref(""),
  notice = ref(""),
  fresh = ref(false);
const status = ref<BookReviewDispositionStatus>("open"),
  note = ref(""),
  acknowledged = ref(false);
const savedForm = ref(JSON.stringify({ status: "open", note: "" }));
let epoch = 0,
  reloadQueued = false;
const unknown = ref<{
  operation: string;
  key: string;
  status: BookReviewDispositionStatus;
  note: string;
  chapters: BookReviewChapterPin[];
  baseRevision: number;
} | null>(null);
const fingerprint = computed(() =>
  JSON.stringify(
    props.report.passes.map((p) => [
      p.step_id,
      p.output_hash,
      p.result.findings.map((f) => f.id),
    ]),
  ),
);
const issues = computed(() =>
  props.report.passes.flatMap((pass, passIndex) =>
    pass.result.findings.map((finding, index) => ({
      key: bookReviewIssueKey(
        props.report.run.id,
        pass.step_id ?? `legacy:${passIndex}`,
        pass.output_hash ?? "",
        finding.id || `legacy:${index}`,
      ),
      pass,
      finding,
      stable: !!pass.step_id && !!pass.output_hash && !!finding.id,
      record: records.value?.entries.find(
        (entry) =>
          entry.source_step_id === pass.step_id &&
          entry.source_output_hash === pass.output_hash &&
          entry.finding_id === finding.id,
      ),
    })),
  ),
);
const selected = computed(() =>
  issues.value.find((issue) => issue.key === focused.value),
);
const dirty = computed(
  () =>
    !!selected.value &&
    JSON.stringify({ status: status.value, note: note.value }) !==
      savedForm.value,
);
const blocked = computed(() => writing.value || dirty.value || !!unknown.value);
const chapterTitle = (id: string) =>
  props.report.preview.chapters.find((chapter) => chapter.id === id)?.title ??
  id;
const chapterLabel = (id: string) => {
  const chapter = props.report.preview.chapters.find((item) => item.id === id);
  return chapter
    ? `${chapter.volume} / ${chapter.title} · ${id.slice(0, 8)}`
    : id;
};
const filtered = computed(() =>
  issues.value.filter((issue) => {
    const entry = issue.record,
      finding = issue.finding,
      q = filters.value.query.trim().toLocaleLowerCase();
    return (
      (!filters.value.severity ||
        finding.severity === filters.value.severity) &&
      (!filters.value.category ||
        finding.category === filters.value.category) &&
      (!filters.value.chapterId ||
        finding.evidence.some(
          (e) => e.chapter_id === filters.value.chapterId,
        )) &&
      (!filters.value.disposition ||
        (filters.value.disposition === "stale"
          ? !!entry?.stale
          : !entry?.stale &&
            (entry?.status ?? "open") === filters.value.disposition)) &&
      (!q ||
        [
          finding.title,
          finding.explanation,
          finding.suggestion,
          finding.id,
          entry?.note ?? "",
          ...finding.evidence.flatMap((e) => [
            chapterTitle(e.chapter_id),
            e.quote,
          ]),
        ].some((text) => text.toLocaleLowerCase().includes(q)))
    );
  }),
);
const categories = computed(() => [
  ...new Set(issues.value.map((issue) => issue.finding.category)),
]);
const page = ref(1),
  pageSize = 50;
const pages = computed(() =>
  Math.max(1, Math.ceil(filtered.value.length / pageSize)),
);
const visible = computed(() =>
  filtered.value.slice((page.value - 1) * pageSize, page.value * pageSize),
);
watch(
  filters,
  () => {
    page.value = 1;
  },
  { deep: true },
);
watch(pages, (value) => {
  page.value = Math.min(page.value, value);
});
const evidenceChapters = computed(() => [
  ...new Set(
    issues.value.flatMap((issue) =>
      issue.finding.evidence.map((e) => e.chapter_id),
    ),
  ),
]);
const confirmed = computed(
  () =>
    issues.value.filter(
      (issue) => issue.record?.status === "confirmed" && !issue.record.stale,
    ).length,
);
const stale = computed(
  () => records.value?.entries.filter((entry) => entry.stale).length ?? 0,
);
const history = computed(
  () =>
    records.value?.entries.filter(
      (entry) =>
        !issues.value.some(
          (issue) =>
            issue.pass.step_id === entry.source_step_id &&
            issue.pass.output_hash === entry.source_output_hash &&
            issue.finding.id === entry.finding_id,
        ),
    ) ?? [],
);
const selectedPins = computed(() => {
  const ids = [
    ...new Set(selected.value?.finding.evidence.map((e) => e.chapter_id) ?? []),
  ];
  return ids.map((id) =>
    records.value?.current_chapters.find((pin) => pin.chapter_id === id),
  );
});
const pinsReady = computed(
  () =>
    selectedPins.value.length > 0 &&
    selectedPins.value.every((pin) => pin?.available),
);
const pinsPresent = computed(
  () =>
    selectedPins.value.length > 0 && selectedPins.value.every((pin) => !!pin),
);
function view() {
  emit("view", {
    runId: props.report.run.id,
    filters: { ...filters.value },
    focusedIssue: focused.value,
  });
}
watch([filters, focused], view, { deep: true, immediate: true });
watch(blocked, (value) => emit("blocking", value), { immediate: true });
function installForm() {
  const entry = selected.value?.record;
  status.value = entry?.status ?? "open";
  note.value = entry?.note ?? "";
  acknowledged.value = false;
  savedForm.value = JSON.stringify({ status: status.value, note: note.value });
}
function focus(key: string) {
  if (blocked.value || props.disabled) return;
  focused.value = key;
  installForm();
}
function valid(value: unknown): value is BookReviewDispositions {
  if (!value || typeof value !== "object") return false;
  const data = value as BookReviewDispositions;
  return (
    typeof data.resource_id === "string" &&
    Number.isSafeInteger(data.revision) &&
    data.revision >= 0 &&
    typeof data.hash === "string" &&
    Array.isArray(data.entries) &&
    Array.isArray(data.current_chapters) &&
    data.entries.every(
      (entry) =>
        entry &&
        typeof entry.source_step_id === "string" &&
        typeof entry.source_output_hash === "string" &&
        typeof entry.finding_id === "string" &&
        Object.hasOwn(bookReviewStatuses, entry.status) &&
        typeof entry.note === "string" &&
        Array.isArray(entry.chapters) &&
        entry.chapters.every(validPin) &&
        typeof entry.stale === "boolean" &&
        typeof entry.reason === "string",
    ) &&
    data.current_chapters.every(validPin)
  );
}
function validPin(value: unknown): value is BookReviewChapterPin {
  if (!value || typeof value !== "object") return false;
  const pin = value as BookReviewChapterPin;
  return (
    [
      pin.chapter_id,
      pin.resource_id,
      pin.hash,
      pin.base_head,
      pin.current_head,
      pin.reason,
    ].every((field) => typeof field === "string") &&
    !!pin.chapter_id &&
    Number.isSafeInteger(pin.revision) &&
    pin.revision >= 0 &&
    typeof pin.available === "boolean"
  );
}
function samePins(left: BookReviewChapterPin[], right: BookReviewChapterPin[]) {
  const signature = (pins: BookReviewChapterPin[]) =>
    JSON.stringify(
      pins
        .map((pin) => [
          pin.chapter_id,
          pin.resource_id,
          pin.revision,
          pin.hash,
          pin.base_head,
          pin.current_head,
          pin.available,
          pin.reason,
        ])
        .sort((a, b) => String(a[0]).localeCompare(String(b[0]))),
    );
  return signature(left) === signature(right);
}
async function load() {
  if (writing.value || !active()) return;
  if (loading.value) {
    reloadQueued = true;
    return;
  }
  const request = ++epoch,
    run = props.report.run.id;
  loading.value = true;
  error.value = "";
  fresh.value = false;
  try {
    const value = await read<unknown>(`${props.root}/${run}/dispositions`);
    if (!active() || request !== epoch || run !== props.report.run.id) return;
    if (!valid(value))
      throw new Error("问题处理记录不完整，请重新读取，原备注仍保留。");
    const previousPins = selectedPins.value.filter(
      (pin): pin is BookReviewChapterPin => !!pin,
    );
    records.value = value;
    const currentPins = selectedPins.value.filter(
      (pin): pin is BookReviewChapterPin => !!pin,
    );
    if (!samePins(previousPins, currentPins)) {
      acknowledged.value = false;
      if (previousPins.length)
        notice.value =
          "证据章稿版本已变化，备注保留；请重新核对当前稿并勾选人工确认。";
    }
    fresh.value = true;
    const pending = unknown.value;
    if (pending) {
      const issue = issues.value.find((item) => item.key === pending.key);
      const entry = issue?.record;
      if (
        value.revision > pending.baseRevision &&
        entry?.operation_id === pending.operation &&
        entry.status === pending.status &&
        entry.note === pending.note &&
        samePins(entry.chapters, pending.chapters)
      ) {
        unknown.value = null;
        installForm();
        notice.value = "已从服务端核实这笔处理记录，未重复提交。";
      } else
        notice.value =
          "尚未核实原提交结果，备注保留。请继续读取核对，不会自动重发或记为已保存。";
    } else if (!dirty.value) installForm();
  } catch (e) {
    if (!active() || request !== epoch || run !== props.report.run.id) return;
    if (e instanceof APIError && [401, 403].includes(e.status)) {
      records.value = null;
      if (e.status === 401) emit("expired");
      emit("denied", e.status);
    }
    error.value = message(e);
  } finally {
    if (active() && request === epoch) {
      loading.value = false;
      if (reloadQueued) {
        reloadQueued = false;
        void load();
      }
    }
  }
}
async function save() {
  const issue = selected.value,
    base = records.value;
  if (
    !issue ||
    !issue.stable ||
    !base ||
    !fresh.value ||
    writing.value ||
    props.disabled ||
    unknown.value ||
    !pinsPresent.value ||
    (status.value === "confirmed" && (!acknowledged.value || !pinsReady.value))
  )
    return;
  const expected = selectedPins.value.filter(
    (pin): pin is BookReviewChapterPin => !!pin,
  );
  const intent = {
    operation: newOperationID(),
    key: issue.key,
    status: status.value,
    note: note.value,
    chapters: expected,
    baseRevision: base.revision,
  };
  ++epoch;
  loading.value = false;
  writing.value = true;
  error.value = "";
  notice.value = "";
  try {
    const value = await api<unknown>(
      `${props.root}/${props.report.run.id}/dispositions`,
      "PUT",
      {
        revision: base.revision,
        hash: base.hash,
        source_step_id: issue.pass.step_id,
        source_output_hash: issue.pass.output_hash,
        finding_id: issue.finding.id,
        status: intent.status,
        note: intent.note,
        expected_chapters: expected,
        operation_id: intent.operation,
      },
    );
    if (!active()) return;
    if (!valid(value))
      throw new Error("处理记录保存回执不完整，需要重新读取核对。");
    const saved = value.entries.find(
      (entry) =>
        entry.source_step_id === issue.pass.step_id &&
        entry.source_output_hash === issue.pass.output_hash &&
        entry.finding_id === issue.finding.id,
    );
    if (
      value.revision <= base.revision ||
      saved?.operation_id !== intent.operation ||
      saved.status !== intent.status ||
      saved.note !== intent.note ||
      !samePins(saved.chapters, intent.chapters)
    )
      throw new Error("处理记录回执与本次提交不一致，需要读取核对。");
    records.value = value;
    fresh.value = true;
    installForm();
    notice.value =
      "处理记录已保存。人工核对只代表作者对所绑定稿件版本的判断，不是模型重新审稿或平台审核结论。";
  } catch (e) {
    if (!active()) return;
    fresh.value = false;
    if (
      e instanceof APIError &&
      [400, 401, 403, 404, 409, 422].includes(e.status)
    ) {
      acknowledged.value = false;
      if ([401, 403].includes(e.status)) {
        records.value = null;
        if (e.status === 401) emit("expired");
        emit("denied", e.status);
      }
      error.value = message(e) + " 请重新读取记录并核对后再保存，备注仍保留。";
    } else {
      unknown.value = intent;
      error.value =
        "提交结果尚未确认，备注仍保留。请先重新读取记录核对；不会自动重发。";
    }
  } finally {
    if (active()) writing.value = false;
  }
}
function edit(chapterId: string, quote: string) {
  const issue = selected.value;
  if (!issue || blocked.value || props.disabled) return;
  view();
  emit("edit", {
    run_id: props.report.run.id,
    step_id: issue.pass.step_id ?? "",
    output_hash: issue.pass.output_hash ?? "",
    finding_id: issue.finding.id,
    chapter_id: chapterId,
    quote,
    reviewed_hash:
      props.report.preview.chapters.find((chapter) => chapter.id === chapterId)
        ?.hash ?? "",
    title: issue.finding.title,
    explanation: issue.finding.explanation,
    suggestion: issue.finding.suggestion,
  });
}
watch([fingerprint, () => props.report], () => {
  acknowledged.value = false;
  if (!writing.value) void load();
});
let restored = false;
watch(
  issues,
  () =>
    void nextTick(() => {
      if (restored || !focused.value) return;
      const index = filtered.value.findIndex(
        (issue) => issue.key === focused.value,
      );
      if (index < 0) return;
      page.value = Math.floor(index / pageSize) + 1;
      void nextTick(() => {
        const element = Array.from(
          document.querySelectorAll<HTMLElement>("[data-review-issue]"),
        ).find((item) => item.dataset.reviewIssue === focused.value);
        if (element) {
          element.scrollIntoView({ block: "center" });
          restored = true;
        }
      });
    }),
  { immediate: true },
);
onMounted(() => void load());
</script>

<template>
  <section class="review-issues" aria-label="审稿问题工作台">
    <header>
      <h2>审稿问题工作台</h2>
      <button :disabled="loading || writing || disabled" @click="load">
        重新读取处理记录
      </button>
    </header>
    <p>
      共 {{ issues.length }} 项问题 · 优先修订
      {{ issues.filter((issue) => issue.finding.severity === "major").length }}
      项 · 有效人工核对 {{ confirmed }} 项 · 历史处理需重核
      {{ stale }} 项。当前筛选显示 {{ filtered.length }} 项。
    </p>
    <p>
      人工处理记录独立于原审稿报告。改稿、证据变版或审稿尝试变化后，旧判断需要重新核对；原文引句消失不等于问题解决。
    </p>
    <p v-if="loading" role="status">正在读取问题处理记录…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <div class="issue-filters">
      <label
        >搜索问题<input
          v-model="filters.query"
          type="search"
          aria-label="搜索审稿问题"
          placeholder="问题、建议、原文或章节"
          maxlength="300" /></label
      ><label
        >严重程度<select v-model="filters.severity" aria-label="问题严重程度">
          <option value="">全部</option>
          <option
            v-for="(label, key) in bookReviewSeverities"
            :key="key"
            :value="key"
          >
            {{ label }}
          </option>
        </select></label
      ><label
        >问题类别<select v-model="filters.category" aria-label="问题类别">
          <option value="">全部</option>
          <option
            v-for="category in categories"
            :key="category"
            :value="category"
          >
            {{ bookReviewCategories[category] ?? category }}
          </option>
        </select></label
      ><label
        >证据章节<select v-model="filters.chapterId" aria-label="问题证据章节">
          <option value="">全部</option>
          <option v-for="id in evidenceChapters" :key="id" :value="id">
            {{ chapterLabel(id) }}
          </option>
        </select></label
      ><label
        >处理状态<select
          v-model="filters.disposition"
          aria-label="问题处理状态"
        >
          <option value="">全部</option>
          <option
            v-for="(label, key) in bookReviewStatuses"
            :key="key"
            :value="key"
          >
            {{ label }}
          </option>
          <option value="stale">旧判断待重核</option>
        </select></label
      >
    </div>
    <p v-if="!filtered.length">当前筛选没有匹配问题。</p>
    <p>
      第 {{ page }} / {{ pages }} 页 · 本页 {{ visible.length }} 项（每页最多
      {{ pageSize }} 项）
    </p>
    <div>
      <button :disabled="page <= 1" @click="page--">上一页问题</button
      ><button :disabled="page >= pages" @click="page++">下一页问题</button>
    </div>
    <article
      v-for="issue in visible"
      :key="issue.key"
      :data-review-issue="issue.key"
      class="issue-card"
      :class="{ focused: issue.key === focused }"
    >
      <h3>
        {{
          bookReviewSeverities[issue.finding.severity] ?? issue.finding.severity
        }}
        · {{ issue.finding.title }}
      </h3>
      <p>
        {{
          bookReviewCategories[issue.finding.category] ?? issue.finding.category
        }}
        ·
        {{
          issue.record?.stale
            ? "旧判断待重核"
            : bookReviewStatuses[issue.record?.status ?? "open"]
        }}
      </p>
      <p>{{ issue.finding.explanation }}</p>
      <p>建议：{{ issue.finding.suggestion }}</p>
      <p v-if="issue.record?.stale" role="status">{{ issue.record.reason }}</p>
      <p v-if="!issue.stable">
        旧报告缺少完整问题版本标识，可读报告并修改正文，暂不能保存与此版本绑定的处理记录。
      </p>
      <button :disabled="blocked || disabled" @click="focus(issue.key)">
        定位并处理此问题
      </button>
    </article>
    <section
      v-if="selected"
      class="panel issue-treatment"
      aria-label="当前问题处理"
    >
      <h3>{{ selected.finding.title }}</h3>
      <blockquote v-for="(e, index) in selected.finding.evidence" :key="index">
        <p>《{{ chapterTitle(e.chapter_id) }}》：{{ e.quote }}</p>
        <button
          :disabled="blocked || disabled"
          @click="edit(e.chapter_id, e.quote)"
        >
          打开《{{ chapterTitle(e.chapter_id) }}》定位编辑
        </button>
      </blockquote>
      <p v-if="selected.record?.stale" role="status">
        旧处理记录需要重新核对：{{ selected.record.reason }}
      </p>
      <fieldset :disabled="writing || disabled || !!unknown">
        <label
          >人工处理状态<select
            v-model="status"
            aria-label="当前问题人工处理状态"
          >
            <option
              v-for="(label, key) in bookReviewStatuses"
              :key="key"
              :value="key"
            >
              {{ label }}
            </option>
          </select></label
        ><label
          >处理备注<textarea
            v-model="note"
            aria-label="问题处理备注"
            maxlength="2000"
            rows="4"
            placeholder="记录改了哪里、判断依据或暂缓原因"
          /></label
        ><label v-if="status === 'confirmed'" class="ack"
          ><input
            v-model="acknowledged"
            type="checkbox"
          />我已人工核对本问题全部证据章节的当前已保存稿；这是作者判断。</label
        >
      </fieldset>
      <p v-if="!pinsReady">
        本问题部分证据章节当前不可读取，不能将不完整范围标为已核对。
      </p>
      <p v-if="dirty">
        处理备注尚未保存；保存或撤回本次备注后才能切换报告或进入正文。
      </p>
      <button
        :disabled="
          writing ||
          loading ||
          disabled ||
          !fresh ||
          !selected.stable ||
          !pinsPresent ||
          !!unknown ||
          (status === 'confirmed' && (!acknowledged || !pinsReady))
        "
        @click="save"
      >
        {{ writing ? "正在保存处理记录…" : "保存人工处理记录" }}</button
      ><button
        :disabled="writing || !!unknown || disabled"
        @click="installForm"
      >
        撤回未保存的处理备注
      </button>
    </section>
    <details v-if="history.length">
      <summary>当前报告之外的历史处理记录（{{ history.length }} 项）</summary>
      <article v-for="(entry, index) in history" :key="index">
        <p>
          {{ entry.finding_id }} · {{ bookReviewStatuses[entry.status] }} ·
          {{ entry.stale ? "旧判断待重核" : "不属于当前显示的报告输出" }}
        </p>
        <p>{{ entry.reason }}</p>
        <p class="preserve">{{ entry.note }}</p>
      </article>
    </details>
  </section>
</template>

<style scoped>
.review-issues {
  display: grid;
  gap: 1rem;
}
.review-issues header {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
  align-items: center;
  justify-content: space-between;
}
.issue-filters {
  display: grid;
  grid-template-columns: 2fr repeat(4, 1fr);
  gap: 0.75rem;
}
.issue-filters label,
.issue-treatment label {
  display: grid;
  gap: 0.35rem;
}
.issue-card {
  padding: 1rem;
  border: 1px solid var(--border, #ddd);
  border-radius: 0.5rem;
}
.issue-card.focused {
  border-color: #648077;
}
.issue-treatment fieldset {
  display: grid;
  gap: 1rem;
}
.issue-treatment blockquote {
  margin: 0.75rem 0;
  border-left: 3px solid #8a9e80;
  padding-left: 1rem;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.issue-treatment .ack {
  display: flex;
  align-items: flex-start;
  gap: 0.5rem;
}
.ack input {
  width: auto;
}
.preserve {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
@media (max-width: 850px) {
  .issue-filters {
    grid-template-columns: 1fr 1fr;
  }
}
@media (max-width: 540px) {
  .issue-filters {
    grid-template-columns: 1fr;
  }
}
</style>
