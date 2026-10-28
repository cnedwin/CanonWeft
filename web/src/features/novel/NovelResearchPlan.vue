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
import type { ResearchDraft } from "./researchTypes";
import type { Branch } from "../ip/types";
import ImportPanel from "../ip/ImportPanel.vue";
import {
  researchSources,
  researchSourceLabel,
  safeResearchLink,
} from "../ip/researchSources";
interface Hit {
  page_id: number;
  title: string;
  snippet: string;
  url: string;
  selected: boolean;
  import_id?: string;
}
interface Query {
  query: string;
  purpose: string;
  preferred_source: string;
  done: boolean;
  hits: Hit[];
}
interface Run {
  background_queued: boolean;
  resource_id: string;
  revision: number;
  document: {
    provider?: string;
    background?: boolean;
    title: string;
    notebook_revision: number;
    language: string;
    stage: string;
    paused: boolean;
    cancelled: boolean;
    attempt?: string;
    lease_until?: number;
    error?: string;
    queries: Query[];
  };
}
const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
  branch: Branch;
  notebook: ResearchDraft;
  blocked: boolean;
}>();
const emit = defineEmits<{ blocking: [boolean]; expired: []; prepared: [] }>();
const root = `/novels/${props.book.id}/research/${props.notebook.resource_id}/runs`;
const runs = ref<Run[]>([]),
  run = ref<Run | null>(null),
  language = ref("zh"),
  provider = ref("wikipedia"),
  autoCollect = ref(2),
  busy = ref(false),
  controlling = ref(false),
  importBusy = ref(false),
  importID = ref(""),
  error = ref("");
const selectionBaseline = ref("");
const sourceLanguages = computed(
  () =>
    researchSources.find((source) => source.id === provider.value)?.languages ??
    [],
);
function changeProvider() {
  if (!sourceLanguages.value.includes(language.value))
    language.value = sourceLanguages.value[0] ?? "en";
}
let operation = newOperationID(),
  disposed = false;
let pollTimer: ReturnType<typeof setTimeout> | undefined;
function selections() {
  return (
    run.value?.document.queries.flatMap((q, qi) =>
      q.hits
        .filter((h) => h.selected)
        .map((h) => ({ query_index: qi, page_id: h.page_id })),
    ) ?? []
  );
}
const selectionDirty = computed(
  () => JSON.stringify(selections()) !== selectionBaseline.value,
);
const locked = computed(
  () => props.blocked || busy.value || controlling.value || importBusy.value,
);
const progress = computed(() =>
  run.value
    ? `${run.value.document.queries.filter((q) => q.done).length} / ${run.value.document.queries.length}`
    : "",
);
const state = computed(() => {
  const d = run.value?.document;
  if (!d) return "";
  if (d.cancelled) return "已取消";
  if (d.paused) return "已暂停";
  if (d.error) return "需要重试";
  if (d.attempt) return "当前请求处理中；中断后最多一分钟可重试";
  if (d.background && ["search", "collect"].includes(d.stage))
    return run.value?.background_queued
      ? "后台已排队，将自动继续"
      : "后台等待续跑，请点击继续";
  return (
    {
      search: "待继续检索",
      selection: "检索完成，请选择资料",
      collect: "待继续采集",
      complete: "选中资料已采集",
    } as Record<string, string>
  )[d.stage];
});
watch(
  [busy, controlling, importBusy, selectionDirty],
  () =>
    emit(
      "blocking",
      busy.value ||
        controlling.value ||
        importBusy.value ||
        selectionDirty.value,
    ),
  { flush: "sync" },
);
function fail(e: unknown) {
  if (!disposed) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  }
}
function accept(r: Run) {
  if (disposed) return;
  run.value = r;
  selectionBaseline.value = JSON.stringify(selections());
}
async function history() {
  const r = await api<{ items: Run[] }>(root);
  if (!disposed) {
    runs.value = r.items;
    if (!run.value && r.items[0]) accept(r.items[0]);
  }
}
async function load(id: string) {
  if (locked.value || selectionDirty.value) return;
  busy.value = true;
  error.value = "";
  try {
    accept(await api<Run>(`${root}/${id}`));
    importID.value = "";
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function create() {
  if (locked.value || selectionDirty.value) return;
  busy.value = true;
  error.value = "";
  try {
    accept(
      await api<Run>(root, "POST", {
        revision: props.notebook.revision,
        hash: props.notebook.hash,
        language: language.value,
        provider: provider.value === "wikipedia" ? "" : provider.value,
        auto_collect_limit: autoCollect.value,
        operation_id: operation,
      }),
    );
    operation = newOperationID();
    importID.value = "";
    await history();
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function advance() {
  if (
    !run.value ||
    locked.value ||
    selectionDirty.value ||
    run.value.document.cancelled
  )
    return;
  busy.value = true;
  error.value = "";
  try {
    accept(
      await api<Run>(`${root}/${run.value.resource_id}/background`, "POST", {
        revision: run.value.revision,
      }),
    );
    if (!disposed) await history();
  } catch (e) {
    fail(e);
    if (!disposed && run.value) {
      try {
        accept(await api<Run>(`${root}/${run.value.resource_id}`));
      } catch (e) {
        fail(e);
      }
    }
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function control(action: "pause" | "cancel") {
  if (
    !run.value ||
    controlling.value ||
    props.blocked ||
    importBusy.value ||
    selectionDirty.value
  )
    return;
  controlling.value = true;
  error.value = "";
  try {
    accept(
      await api<Run>(`${root}/${run.value.resource_id}/control`, "POST", {
        action,
      }),
    );
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) controlling.value = false;
  }
}
async function saveSelection() {
  if (!run.value || locked.value) return;
  busy.value = true;
  error.value = "";
  try {
    accept(
      await api<Run>(`${root}/${run.value.resource_id}/selection`, "PUT", {
        revision: run.value.revision,
        items: selections(),
      }),
    );
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function safeLink(url: string) {
  return safeResearchLink(url);
}
onMounted(() => {
  void history().catch(fail);
  pollTimer = setTimeout(poll, 2000);
});
async function poll() {
  if (disposed) return;
  const current = run.value;
  try {
    if (
      current?.document.background &&
      !locked.value &&
      !selectionDirty.value &&
      !current.document.paused &&
      !current.document.cancelled &&
      ["search", "collect"].includes(current.document.stage) &&
      !current.document.error
    ) {
      const result = await api<Run>(`${root}/${current.resource_id}`);
      if (
        !disposed &&
        run.value?.resource_id === current.resource_id &&
        !locked.value &&
        !selectionDirty.value &&
        result.revision >= run.value.revision
      )
        accept(result);
    }
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) pollTimer = setTimeout(poll, 2000);
  }
}
onBeforeUnmount(() => {
  disposed = true;
  clearTimeout(pollTimer);
  emit("blocking", false);
});
</script>
<template>
  <section class="panel research-execution" aria-label="执行检索计划">
    <h2>执行检索计划</h2>
    <p>
      按已保存的检索词逐条搜索，选中结果后批量采集。新任务固定所选免费来源；优先来源建议仍须核对，检索词会发送给公开服务，请勿包含私人原稿。开放论文须用英语，采集逐篇检查许可；名字条目需检查类型。
    </p>
    <p>
      点击继续后由后台逐步执行，关闭页面仍会推进；任务服务停止时保留进度，重启后继续。暂停或取消不会撤回已保存原件，也不会自动采用资料。从备份恢复后须重新点击继续。
    </p>
    <label
      >新任务来源<select
        v-model="provider"
        :disabled="locked || selectionDirty"
        @change="changeProvider"
      >
        <option
          v-for="source in researchSources"
          :key="source.id"
          :value="source.id"
        >
          {{ source.label }}
        </option>
      </select></label
    >
    <label
      >检索语言<select v-model="language" :disabled="locked || selectionDirty">
        <option v-for="lang in sourceLanguages" :key="lang" :value="lang">
          {{ lang }}
        </option>
      </select></label
    >
    <label
      >新任务采集方式<select
        v-model.number="autoCollect"
        :disabled="locked || selectionDirty"
      >
        <option :value="2">自动采集每项前两条线索</option>
        <option :value="1">自动采集每项第一条线索</option>
        <option :value="0">由我选择需要采集的资料</option>
      </select></label
    >
    <p>
      自动模式按公开检索顺序取结果，最多每项两条；排序不代表可靠性，原件仍须核对后才能采用。
    </p>
    <button
      :disabled="
        locked || selectionDirty || !notebook.document.search_plan?.length
      "
      @click="create"
    >
      建立本版检索任务
    </button>
    <label v-if="runs.length"
      >已保存检索任务<select
        :value="run?.resource_id"
        :disabled="locked || selectionDirty"
        @change="load(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="r in runs" :key="r.resource_id" :value="r.resource_id">
          {{ r.document.title }} · 研究稿第{{ r.document.notebook_revision }}版
          · {{ researchSourceLabel(r.document.provider) }} ·
          {{ r.document.language }} · {{ r.resource_id.slice(-6) }}
        </option>
      </select></label
    >
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <template v-if="run">
      <p role="status">{{ state }} · 已检索 {{ progress }}</p>
      <p v-if="run.document.error" role="alert">{{ run.document.error }}</p>
      <button
        :disabled="
          locked ||
          selectionDirty ||
          run.document.cancelled ||
          (!!run.document.attempt &&
            (run.document.lease_until ?? 0) * 1000 > Date.now()) ||
          (run.background_queued &&
            !run.document.paused &&
            !run.document.error) ||
          !['search', 'collect'].includes(run.document.stage)
        "
        @click="advance"
      >
        继续检索或采集
      </button>
      <button
        :disabled="
          controlling ||
          blocked ||
          importBusy ||
          selectionDirty ||
          run.document.paused ||
          run.document.cancelled
        "
        @click="control('pause')"
      >
        暂停当前任务
      </button>
      <button
        :disabled="
          controlling ||
          blocked ||
          importBusy ||
          selectionDirty ||
          run.document.cancelled
        "
        @click="control('cancel')"
      >
        取消后续步骤
      </button>
      <button
        :disabled="locked || selectionDirty"
        @click="load(run.resource_id)"
      >
        刷新任务进度
      </button>
      <article
        v-for="(q, qi) in run.document.queries"
        :key="qi"
        :aria-label="`检索项 ${qi + 1}`"
      >
        <h3>{{ qi + 1 }}. {{ q.query }}</h3>
        <p>{{ q.purpose }} · 优先来源建议：{{ q.preferred_source }}</p>
        <p v-if="!q.done">尚未完成检索</p>
        <p v-else-if="!q.hits.length">
          未找到资料，可修改研究计划后建立新任务。
        </p>
        <ul>
          <li v-for="h in q.hits" :key="h.page_id">
            <label
              ><input
                v-model="h.selected"
                type="checkbox"
                :disabled="
                  locked ||
                  run.document.cancelled ||
                  run.document.stage === 'search'
                "
              />选择 {{ h.title }}</label
            >
            <a :href="safeLink(h.url)" target="_blank" rel="noopener noreferrer"
              >查看 {{ h.title }}</a
            >
            <p>{{ h.snippet }}</p>
            <button
              v-if="h.import_id"
              :disabled="locked || selectionDirty"
              @click="importID = h.import_id"
            >
              编辑采集稿：{{ h.title }}
            </button>
          </li>
        </ul>
      </article>
      <button
        :disabled="locked || !selectionDirty || run.document.cancelled"
        @click="saveSelection"
      >
        保存资料选择
      </button>
      <p v-if="selectionDirty">资料选择尚未保存。</p>
    </template>
    <section v-if="importID" aria-label="研究资料编辑">
      <h3>采集稿编辑与入库</h3>
      <ImportPanel
        :key="importID"
        :project="project"
        :actor="actor"
        :branch="branch"
        :blocked="blocked || busy || controlling"
        :initially-open="true"
        :initial-source-id="importID"
        @blocking="importBusy = $event"
        @expired="emit('expired')"
        @prepared="emit('prepared')"
      />
      <button
        :disabled="importBusy || busy || controlling"
        @click="importID = ''"
      >
        关闭采集稿编辑
      </button>
    </section>
  </section>
</template>
<style scoped>
.research-execution {
  margin: 18px 0;
}
article {
  border-top: 1px solid #d8e3dc;
  margin: 16px 0;
  padding: 12px 0;
}
label {
  display: block;
  margin: 8px 0;
}
input[type="checkbox"] {
  display: inline-block;
  width: auto;
  margin-right: 6px;
}
select {
  max-width: 100%;
}
button {
  margin: 4px 8px 4px 0;
}
li {
  margin: 12px 0;
}
</style>
