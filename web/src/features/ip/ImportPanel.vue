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
import type { Branch } from "./types";
import SourcePolicyEditor from "./SourcePolicyEditor.vue";
import ResearchSourcePicker from "./ResearchSourcePicker.vue";
import { defaultPolicy, type KnowledgePolicy } from "./knowledgePolicy";
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: Branch;
  blocked: boolean;
  initiallyOpen?: boolean;
  initialSourceId?: string;
}>();
const emit = defineEmits<{ prepared: []; blocking: [boolean]; expired: [] }>();
interface Unit {
  locator: string;
  kind: string;
  status: string;
  reason?: string;
  text?: string;
  confidence?: number;
}
interface Source {
  id: string;
  name: string;
  hash: string;
  report: {
    complete: boolean;
    extracted: number;
    pending: number;
    text: string;
    units: Unit[];
  };
}
interface Job {
  id: string;
  name: string;
  state: string;
  paused: boolean;
  cancelled: boolean;
  revision: number;
  reason: string;
  progress: { plan: { count: number } | null; units: Unit[] | null };
}
interface Draft {
  policy?: KnowledgePolicy;
  base_ip_head: string;
  text: string;
  operation_id: string;
  accept_partial: boolean;
  closed: boolean;
}
interface DraftResponse {
  revision: number;
  content: Draft;
  stale: boolean;
}
const root = `/projects/${props.project.id}/branches/${props.branch.id}/imports`;
const storageKey = `studio-import:${props.actor.id}:${props.branch.id}`;
const source = ref<Source | null>(null),
  job = ref<Job | null>(null),
  jobs = ref<Job[]>([]);
const content = ref<Draft>({
  base_ip_head: props.branch.head,
  text: "",
  operation_id: newOperationID(),
  accept_partial: false,
  closed: false,
});
const busy = ref(false),
  loading = ref(false),
  saving = ref(false),
  error = ref(""),
  notice = ref(""),
  saveError = ref(""),
  conflict = ref(false),
  stale = ref(false);
const readFailed = ref(false),
  readID = ref("");
const reviewedBranch = ref<Branch | null>(null);
const researchBusy = ref(false);
async function selectResearchSource(id: string) {
  if (
    busy.value ||
    loading.value ||
    saving.value ||
    unsaved.value ||
    props.blocked
  )
    return;
  job.value = null;
  localStorage.setItem(storageKey, id);
  await restoreSource(id);
}
const savedJSON = ref("");
let revision = 0,
  disposed = false,
  epoch = 0;
let timer: ReturnType<typeof setTimeout> | undefined,
  pollTimer: ReturnType<typeof setTimeout> | undefined,
  pending: Promise<void> | undefined;
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const draftJSON = computed(() => JSON.stringify(content.value));
const unsaved = computed(
  () => !!source.value && !loading.value && draftJSON.value !== savedJSON.value,
);
const text = computed({
  get: () => content.value.text,
  set: (v: string) => {
    content.value.text = v;
    content.value.operation_id = newOperationID();
  },
});
const acceptPartial = computed({
  get: () => content.value.accept_partial,
  set: (v: boolean) => {
    content.value.accept_partial = v;
    content.value.operation_id = newOperationID();
  },
});
const saved = computed(() => content.value.closed);
const running = computed(
  () =>
    job.value &&
    !["COMPLETED", "FAILED", "CANCELLED"].includes(job.value.state),
);
const locked = computed(
  () =>
    busy.value ||
    loading.value ||
    readFailed.value ||
    props.blocked ||
    researchBusy.value,
);
const draftStatus = computed(() =>
  loading.value
    ? "正在恢复导入草稿…"
    : saveError.value
      ? "导入草稿暂存失败，本页修改仍保留"
      : saving.value || unsaved.value
        ? "正在自动暂存导入草稿…"
        : "导入草稿已自动暂存",
);
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
const live = (ticket: number) => !disposed && ticket === epoch;
async function refreshJobs() {
  const result = await api<{ items: Job[] }>(`${root}/jobs`);
  if (!disposed) jobs.value = result.items;
}
async function restoreSource(id: string) {
  const ticket = ++epoch;
  reviewedBranch.value = null;
  readID.value = id;
  readFailed.value = false;
  error.value = "";
  clearTimeout(timer);
  pending = undefined;
  saving.value = false;
  loading.value = true;
  try {
    const [original, draft] = await Promise.all([
      api<Source>(`${root}/${id}`),
      api<DraftResponse>(`${root}/${id}/draft`),
    ]);
    if (!live(ticket)) return;
    source.value = original;
    content.value = draft.content;
    // Missing policy is a legacy evidence draft; preserve that interpretation.
    content.value.policy ??= defaultPolicy();
    revision = draft.revision;
    savedJSON.value = draftJSON.value;
    stale.value = draft.stale;
    saveError.value = "";
    conflict.value = false;
    loading.value = false;
    notice.value = "已恢复导入结果和私人修改；正式保存为候选前可继续编辑。";
  } catch (e) {
    if (live(ticket)) {
      loading.value = false;
      readFailed.value = true;
      fail(e);
    }
  }
}
async function persist() {
  clearTimeout(timer);
  if (pending) return pending;
  if (!source.value || loading.value || !unsaved.value) return;
  if (conflict.value)
    throw new Error("另一页面已保存新版本，请先处理导入草稿冲突。");
  const ticket = epoch,
    id = source.value.id;
  saving.value = true;
  saveError.value = "";
  pending = (async () => {
    try {
      while (live(ticket) && unsaved.value) {
        const snapshot = draftJSON.value;
        const result = await api<DraftResponse>(`${root}/${id}/draft`, "PUT", {
          revision,
          content: JSON.parse(snapshot),
        });
        if (!live(ticket)) return;
        revision = result.revision;
        savedJSON.value = snapshot;
        stale.value = result.stale;
      }
    } catch (e) {
      if (live(ticket)) {
        saveError.value = message(e);
        conflict.value = e instanceof APIError && e.status === 409;
        if (e instanceof APIError && e.status === 401) emit("expired");
      }
      throw e;
    } finally {
      if (live(ticket)) {
        saving.value = false;
        pending = undefined;
      }
    }
  })();
  return pending;
}
function retrySave() {
  void persist().catch(() => {});
}
watch(
  draftJSON,
  () => {
    if (disposed || loading.value || !source.value) return;
    clearTimeout(timer);
    if (!saveError.value) timer = setTimeout(retrySave, 350);
  },
  { flush: "sync" },
);
watch(
  () => props.branch.head,
  (head) => {
    if (source.value && head !== content.value.base_ip_head) {
      stale.value = true;
      reviewedBranch.value = null;
    }
  },
);
watch([busy, loading, saving, unsaved, researchBusy], () =>
  emit(
    "blocking",
    busy.value ||
      loading.value ||
      saving.value ||
      unsaved.value ||
      researchBusy.value,
  ),
);
function guard(event: BeforeUnloadEvent) {
  if (unsaved.value || busy.value || saving.value || researchBusy.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
function schedulePoll() {
  clearTimeout(pollTimer);
  if (!disposed && running.value)
    pollTimer = setTimeout(() => {
      void poll();
    }, 800);
}
async function poll() {
  const id = job.value?.id;
  if (!id || disposed) return;
  try {
    const current = await api<Job>(`${root}/jobs/${id}`);
    if (
      disposed ||
      job.value?.id !== id ||
      current.revision < job.value.revision
    )
      return;
    job.value = current;
    if (current.state === "COMPLETED" && source.value?.id !== id) {
      await restoreSource(id);
      await refreshJobs();
    }
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    schedulePoll();
  }
}
async function selectJob(id: string) {
  if (busy.value || saving.value || unsaved.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  clearTimeout(pollTimer);
  ++epoch;
  source.value = null;
  loading.value = false;
  readFailed.value = false;
  readID.value = "";
  try {
    job.value = await api<Job>(`${root}/jobs/${id}`);
    if (disposed) return;
    localStorage.setItem(storageKey, id);
    if (job.value.state === "COMPLETED") await restoreSource(id);
    else schedulePoll();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function upload(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0];
  if (!file || locked.value || saving.value || unsaved.value) return;
  if (file.size > 32 * 1024 * 1024) {
    error.value = "文件超过 32 MiB，请分批导入。";
    return;
  }
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await persist();
    const body = new FormData();
    body.append("file", file);
    const response = await fetch(`/api${root}/jobs`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Idempotency-Key": newOperationID() },
      body,
    });
    const result = await response.json();
    if (!response.ok)
      throw new APIError(result.error ?? "上传失败", response.status);
    if (disposed) return;
    ++epoch;
    source.value = null;
    readFailed.value = false;
    readID.value = "";
    job.value = result as Job;
    localStorage.setItem(storageKey, job.value.id);
    notice.value = "原文件已保存，正在后台处理。可以离开页面，稍后继续。";
    await refreshJobs();
    schedulePoll();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function control(action: string) {
  if (!job.value || locked.value) return;
  busy.value = true;
  error.value = "";
  try {
    job.value = await api<Job>(`${root}/jobs/${job.value.id}/control`, "POST", {
      action,
      revision: job.value.revision,
    });
    if (disposed) return;
    await refreshJobs();
    schedulePoll();
  } catch (e) {
    if (!disposed) fail(e);
    await poll();
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function prepare() {
  if (
    !source.value ||
    locked.value ||
    saved.value ||
    stale.value ||
    conflict.value
  )
    return;
  busy.value = true;
  error.value = "";
  try {
    await persist();
    await api(`${root}/${source.value.id}/candidate`, "POST", {
      text: content.value.text,
      expected_ip_head: content.value.base_ip_head,
      operation_id: content.value.operation_id,
      accept_partial: content.value.accept_partial,
      policy: content.value.policy,
    });
    if (disposed) return;
    content.value.closed = true;
    await persist();
    notice.value = "文件内容已保存为候选，请在下方检查、确认采用并发布。";
    emit("prepared");
  } catch (e) {
    if (!disposed) {
      fail(e);
      if (e instanceof APIError && e.status === 409) {
        try {
          const latest = await api<Branch>(root.slice(0, -"/imports".length));
          if (!disposed && latest.head !== content.value.base_ip_head) {
            stale.value = true;
            reviewedBranch.value = null;
          }
        } catch {
          // Keep the candidate error and all edits. The explicit review action retries.
        }
      }
    }
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function inspectLatestSetting() {
  if (locked.value || saved.value || conflict.value) return;
  busy.value = true;
  error.value = "";
  reviewedBranch.value = null;
  try {
    await persist();
    const latest = await api<Branch>(root.slice(0, -"/imports".length));
    if (!disposed) reviewedBranch.value = latest;
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function confirmSetting() {
  if (!reviewedBranch.value || locked.value || conflict.value || saved.value)
    return;
  busy.value = true;
  error.value = "";
  try {
    await persist();
    content.value.base_ip_head = reviewedBranch.value.head;
    content.value.operation_id = newOperationID();
    await persist();
    if (disposed) return;
    reviewedBranch.value = null;
    notice.value = stale.value
      ? "核对期间设定又有更新，文字仍保留，请再次查看最新设定。"
      : "已按你核对的设定版本继续，原文件与编辑内容均保留。";
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function dismiss() {
  if (locked.value) return;
  busy.value = true;
  try {
    await persist();
    ++epoch;
    clearTimeout(pollTimer);
    job.value = null;
    source.value = null;
    savedJSON.value = "";
    localStorage.removeItem(storageKey);
    notice.value = "原文件和私人修改已保留，可从导入任务中重新打开。";
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", guard);
  try {
    await refreshJobs();
    const id = props.initialSourceId || localStorage.getItem(storageKey);
    if (!id || disposed) return;
    if (jobs.value.some((j) => j.id === id)) await selectJob(id);
    else await restoreSource(id);
  } catch (e) {
    if (!disposed) fail(e);
  }
});
onBeforeUnmount(() => {
  disposed = true;
  ++epoch;
  clearTimeout(timer);
  clearTimeout(pollTimer);
  window.removeEventListener("beforeunload", guard);
  emit("blocking", false);
});
</script>
<template>
  <details v-if="canEdit" class="source-import" :open="initiallyOpen">
    <summary>从文件导入资料</summary>
    <ResearchSourcePicker
      :project-id="project.id"
      :branch-id="branch.id"
      :blocked="busy || loading || saving || unsaved || blocked"
      @selected="selectResearchSource"
      @blocking="researchBusy = $event"
      @expired="emit('expired')"
    />
    <p class="muted">
      支持 TXT、Markdown、DOCX、PDF 和 PNG/JPEG/WebP。PDF
      逐页提取文字，扫描页与图片在本机识别中英文。 OCR
      仅识别文字，不理解图像内容；请核对原稿。Word 中的图像仍需另行导入。
      原文件保留，最多 100 页、32 MiB，单次文字上限 256 KiB。
    </p>
    <label
      >上传资料文件<input
        type="file"
        accept=".txt,.md,.docx,.pdf,.png,.jpg,.jpeg,.webp"
        :disabled="locked || saving || unsaved"
        @change="upload"
    /></label>
    <details>
      <summary>导入任务与历史（最近100项）</summary>
      <button :disabled="busy" @click="refreshJobs().catch(fail)">
        刷新导入任务
      </button>
      <ul>
        <li v-for="item in jobs" :key="item.id">
          <button
            :disabled="busy || saving || unsaved"
            @click="selectJob(item.id)"
          >
            {{ item.name }} ·
            {{
              item.state === "COMPLETED"
                ? "可编辑"
                : item.paused
                  ? "已暂停"
                  : item.state === "CANCELLED"
                    ? "已取消"
                    : item.state === "FAILED"
                      ? "未完成"
                      : "处理中"
            }}
          </button>
        </li>
      </ul>
    </details>
    <section v-if="job" data-testid="ingest-progress">
      <a :href="`/api${root}/jobs/${encodeURIComponent(job.id)}/file`" download>
        下载原文件核对
      </a>
      <p>
        {{ job.name }} · 已处理 {{ job.progress.units?.length ?? 0 }} /
        {{ job.progress.plan?.count ?? "待检查" }} 个单元
      </p>
      <p v-if="job.state === 'FAILED'" role="alert">
        {{
          job.reason === "corrupt"
            ? "文件损坏或格式与扩展名不符。"
            : job.reason === "encrypted"
              ? "文件已加密，请先在本地解密。"
              : "文件未能可靠解析，请核对格式、编码或拆分文件。"
        }}原文件仍保留。
      </p>
      <p v-if="job.state === 'CANCELLED'">
        任务已取消，原文件和已处理记录仍保留。
      </p>
      <div v-if="running">
        <button
          :disabled="locked"
          @click="control(job.paused ? 'resume' : 'pause')"
        >
          {{ job.paused ? "继续处理文件" : "暂停处理文件" }}</button
        ><button :disabled="locked" @click="control('cancel')">
          取消文件处理
        </button>
      </div>
    </section>
    <div v-if="source">
      <h3>{{ source.name }}</h3>
      <p data-testid="import-draft-status" role="status">{{ draftStatus }}</p>
      <p v-if="stale" class="error">
        设定已有新版本。原有草稿仍保留，保存候选前请核对版本。
      </p>
      <button
        v-if="!saved"
        :disabled="locked || conflict"
        @click="inspectLatestSetting"
      >
        查看最新设定并核对
      </button>
      <section v-if="reviewedBranch" data-testid="import-current-setting">
        <h4>待核对的设定 · {{ reviewedBranch.name }}</h4>
        <p>
          请将下列设定与导入文字对照。确认只更新草稿依据，内容仍可继续修改。
        </p>
        <article
          v-for="entity in reviewedBranch.snapshot.entities"
          :key="entity.id"
        >
          <h5>{{ entity.name }}</h5>
          <p>{{ entity.description }}</p>
          <p v-if="entity.aliases.length">
            别名：{{ entity.aliases.join("、") }}
          </p>
        </article>
        <p v-for="fact in reviewedBranch.snapshot.assertions" :key="fact.id">
          {{
            reviewedBranch.snapshot.entities.find((e) => e.id === fact.subject)
              ?.name ?? fact.subject
          }}
          · {{ fact.predicate }}：{{ fact.value }}
          {{ fact.locked ? "（已锁定）" : "" }}
        </p>
        <p v-for="rule in reviewedBranch.snapshot.rules" :key="rule.id">
          规则：{{ rule.name }} · {{ rule.predicate }} {{ rule.operator }}
          {{ rule.operand }}
        </p>
        <p v-for="event in reviewedBranch.snapshot.events" :key="event.id">
          事件：{{ event.name }} · {{ event.description }}
        </p>
        <p class="muted">
          完整属性、来源和时间条件可在设定库中继续核对；这里不会自动合并或发布。
        </p>
        <button :disabled="locked || conflict" @click="confirmSetting">
          已核对，以此版本继续编辑
        </button>
      </section>
      <div v-if="saveError" role="alert" class="error">
        <p>{{ saveError }}</p>
        <p v-if="conflict">
          另一页面保存了新稿，本页修改仍保留。请先复制需要保留的内容，再读取最新草稿。
        </p>
        <button v-if="!conflict" :disabled="saving" @click="retrySave">
          重试导入草稿暂存</button
        ><button :disabled="busy || saving" @click="restoreSource(source.id)">
          放弃本页修改，读取最新导入草稿
        </button>
      </div>
      <p>
        已提取 {{ source.report.extracted }} 个文字单元 · 待核对
        {{ source.report.pending }} 项
      </p>
      <details>
        <summary>查看完整覆盖报告</summary>
        <div v-for="unit in source.report.units" :key="unit.locator">
          <p>
            {{ unit.locator }}：{{
              unit.status === "extracted" ? "已提取" : "待核对"
            }}
            {{ unit.kind === "ocr" ? "（OCR 文字）" : "" }}
            <span v-if="unit.confidence !== undefined">
              · 最低行置信度 {{ Math.round(unit.confidence * 100) }}%
            </span>
            {{ unit.reason }}
          </p>
          <pre
            v-if="unit.status === 'pending' && unit.text"
            class="pending-text"
            >{{ unit.text }}</pre
          >
        </div>
      </details>
      <label
        >导入内容 · 可修改<textarea
          v-model="text"
          rows="8"
          :disabled="locked || saved"
        />
      </label>
      <SourcePolicyEditor
        v-if="content.policy"
        :policy="content.policy"
        :entities="branch.snapshot.entities"
        :disabled="locked || saved"
        @changed="content.operation_id = newOperationID()"
      />
      <p class="muted">
        创作要求、世界与故事规划使用“项目工作资料”；已写正文与设定可选“作品证据”。
        上传与暂存不会调用模型，只有明确采用并发布的内容才成为正式检索来源。
      </p>
      <a
        v-if="!job"
        :href="`/api${root}/${encodeURIComponent(source.id)}/file`"
        download
      >
        下载原文件核对
      </a>
      <label v-if="!source.report.complete"
        ><input
          v-model="acceptPartial"
          type="checkbox"
          :disabled="locked || saved"
        />我已检查遗漏，只将当前文本保存为候选，保留待核对记录</label
      >
      <div class="actions">
        <button
          :disabled="
            locked ||
            saved ||
            stale ||
            conflict ||
            !text.trim() ||
            (!source.report.complete && !acceptPartial)
          "
          @click="prepare"
        >
          保存文件内容候选</button
        ><button class="quiet" :disabled="busy" @click="dismiss">
          关闭本次导入编辑
        </button>
      </div>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <button
      v-if="readFailed"
      :disabled="busy || loading"
      @click="restoreSource(readID)"
    >
      重试读取导入草稿
    </button>
    <p v-if="notice" role="status">{{ notice }}</p>
  </details>
</template>
<style scoped>
.source-import {
  margin: 1rem 0;
  padding: 1rem;
  border: 1px solid #dcd8cd;
  border-radius: 0.6rem;
  overflow-wrap: anywhere;
}
label {
  margin: 1rem 0;
}
.pending-text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  padding: 0.6rem;
  background: #f4f1e8;
}
.actions {
  display: flex;
  gap: 0.8rem;
  flex-wrap: wrap;
}
</style>
