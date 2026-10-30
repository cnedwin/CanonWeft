<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import type { NovelBook } from "./types";
import type {
  ManuscriptBatch,
  ManuscriptSource,
  ManuscriptPart,
  ManuscriptProgress,
  ManuscriptPreview,
  ManuscriptMatch,
} from "./manuscriptImportTypes";

const props = defineProps<{
  book: NovelBook;
  initialBatchId?: string;
  canEdit: boolean;
}>();
const emit = defineEmits<{
  close: [];
  expired: [];
  selected: [id: string];
  edit: [chapterId: string];
}>();
const root = `/novels/${encodeURIComponent(props.book.id)}/manuscripts`;
const reads = useBoundedReads({
  stopped: "已离开长稿导入。",
  timeout: "读取超时，原稿和导入进度已保留，可以重试。",
});
const batches = ref<ManuscriptBatch[]>([]),
  sources = ref<ManuscriptSource[]>([]),
  batch = ref<ManuscriptBatch | null>(null);
const parts = ref<ManuscriptPart[]>([]),
  selected = ref(""),
  sourceId = ref("");
const preview = ref<ManuscriptPreview | null>(null),
  acceptPartial = ref(false),
  allowDuplicate = ref(false),
  confirmReplace = ref(false);
const busy = ref(false),
  loading = ref(false),
  reading = ref(false),
  running = ref(false),
  stopRequested = ref(false);
const error = ref(""),
  notice = ref(""),
  splitAt = ref<number | null>(null),
  selectedFile = ref<File | null>(null);
const batchAfter = ref(""),
  sourceAfter = ref("");
const textArea = ref<HTMLTextAreaElement | null>(null);
type Pending = {
  kind: "upload" | "create" | "layout" | "apply" | "reparse";
  op: string;
  path: string;
  method: string;
  body: Record<string, unknown>;
  partId?: string;
  sourceId?: string;
  file?: File;
  batchId?: string;
};
const pending = ref<Pending | null>(null),
  checking = ref(false);
let epoch = 0,
  listSequence = 0,
  batchSequence = 0,
  previewSequence = 0,
  disposed = false;
const dirty = computed(
  () =>
    !!batch.value &&
    (JSON.stringify(parts.value) !==
      JSON.stringify(batch.value.document.parts) ||
      acceptPartial.value !== batch.value.document.accept_partial),
);
const locked = computed(
  () => busy.value || running.value || !!pending.value || !props.canEdit,
);
const frozen = computed(
  () => locked.value || !!batch.value?.document.recovered,
);
const part = computed(() => parts.value.find((p) => p.id === selected.value));
const progress = computed(
  () => new Map(batch.value?.progress.map((p) => [p.part_id, p]) ?? []),
);
const finished = (id: string) =>
  ["done", "skipped"].includes(progress.value.get(id)?.status ?? "");
const doneCount = computed(
  () =>
    batch.value?.progress.filter((p) => ["done", "skipped"].includes(p.status))
      .length ?? 0,
);
const source = computed(() =>
  sources.value.find(
    (s) => s.id === (sourceId.value || batch.value?.document.source_id),
  ),
);
const originalURL = computed(
  () =>
    `/api/projects/${props.book.project_id}/branches/${props.book.branch_id}/imports/${sourceId.value || batch.value?.document.source_id}/file`,
);
const blockLeave = computed(
  () => busy.value || running.value || !!pending.value || dirty.value,
);
function fail(cause: unknown) {
  error.value = message(cause);
  if (cause instanceof APIError && [401, 403].includes(cause.status)) {
    epoch++;
    listSequence++;
    batchSequence++;
    previewSequence++;
    batches.value = [];
    sources.value = [];
    batch.value = null;
    parts.value = [];
    preview.value = null;
    selected.value = "";
    sourceId.value = "";
    selectedFile.value = null;
    stopRequested.value = true;
    notice.value = "";
    if (cause.status === 401) emit("expired");
  }
}
function cloneParts(v: ManuscriptPart[]): ManuscriptPart[] {
  return JSON.parse(JSON.stringify(v)) as ManuscriptPart[];
}
function resetLayout() {
  if (batch.value) {
    parts.value = cloneParts(batch.value.document.parts);
    acceptPartial.value = batch.value.document.accept_partial;
    preview.value = null;
  }
}
function acceptBatch(v: ManuscriptBatch) {
  if (
    v.document.book_id !== props.book.id ||
    !Array.isArray(v.document.parts) ||
    !Array.isArray(v.progress)
  )
    throw new Error("导入记录不完整，请重新读取。");
  batch.value = v;
  sourceId.value = v.document.source_id;
  parts.value = cloneParts(v.document.parts);
  acceptPartial.value = v.document.accept_partial;
  if (!parts.value.some((p) => p.id === selected.value))
    selected.value = parts.value[0]?.id ?? "";
  preview.value = null;
  emit("selected", v.id);
}
async function list(more = false, sourceMore = false) {
  const ticket = ++listSequence,
    auth = epoch;
  loading.value = true;
  error.value = "";
  try {
    const [b, s] = await Promise.all([
      reads.read<{ items: ManuscriptBatch[] }>(
        root + (more && batchAfter.value ? `?after=${batchAfter.value}` : ""),
      ),
      reads.read<{ items: ManuscriptSource[] }>(
        root +
          "/sources" +
          (sourceMore && sourceAfter.value
            ? `?after=${sourceAfter.value}`
            : ""),
      ),
    ]);
    if (disposed || ticket !== listSequence || auth !== epoch) return;
    const bs = b.items.slice(0, 50),
      ss = s.items.slice(0, 50);
    batches.value = more ? [...batches.value, ...bs] : bs;
    sources.value = sourceMore ? [...sources.value, ...ss] : ss;
    batchAfter.value = b.items.length > 50 ? bs.at(-1)!.id : "";
    sourceAfter.value = s.items.length > 50 ? ss.at(-1)!.id : "";
  } catch (e) {
    if (!disposed && auth === epoch) fail(e);
  } finally {
    if (!disposed && ticket === listSequence) loading.value = false;
  }
}
async function open(id: string) {
  if (blockLeave.value) return;
  const ticket = ++batchSequence,
    auth = epoch;
  loading.value = true;
  error.value = "";
  try {
    const v = await reads.read<ManuscriptBatch>(`${root}/${id}`);
    if (!disposed && ticket === batchSequence && auth === epoch) acceptBatch(v);
  } catch (e) {
    if (!disposed && auth === epoch) fail(e);
  } finally {
    if (!disposed && ticket === batchSequence) loading.value = false;
  }
}
async function loadPreview(offset = 0) {
  if (!batch.value || !part.value || dirty.value || busy.value) return;
  const id = batch.value.id,
    pid = part.value.id,
    ticket = ++previewSequence,
    auth = epoch;
  reading.value = true;
  error.value = "";
  try {
    const v = await reads.read<ManuscriptPreview>(
      `${root}/${id}/parts/${pid}?offset=${offset}`,
    );
    if (
      !disposed &&
      ticket === previewSequence &&
      auth === epoch &&
      selected.value === pid &&
      batch.value?.id === id
    ) {
      if (v.part_id !== pid) throw new Error("分章预览不匹配，请重新读取。");
      preview.value = v;
      splitAt.value = null;
    }
  } catch (e) {
    if (!disposed && auth === epoch) fail(e);
  } finally {
    if (!disposed && ticket === previewSequence) reading.value = false;
  }
}
function selectPart(id: string) {
  selected.value = id;
  preview.value = null;
  allowDuplicate.value = false;
  confirmReplace.value = false;
  splitAt.value = null;
  previewSequence++;
  void loadPreview();
}
function mergeNext() {
  if (!part.value || frozen.value || finished(part.value.id)) return;
  const p = part.value,
    next = parts.value.find((v) => v.start === p.end);
  if (!next || finished(next.id)) return;
  p.end = next.end;
  parts.value = parts.value.filter((v) => v.id !== next.id);
  preview.value = null;
}
function split() {
  const p = part.value,
    at = splitAt.value;
  if (
    !p ||
    at === null ||
    frozen.value ||
    finished(p.id) ||
    !Number.isInteger(at) ||
    at <= p.start ||
    at >= p.end
  )
    return;
  const index = parts.value.indexOf(p),
    next = {
      ...p,
      id: crypto.randomUUID().replaceAll("-", ""),
      start: at,
      title: p.title + "（续）",
      mode: "new" as const,
    };
  delete next.target;
  p.end = at;
  parts.value.splice(index + 1, 0, next);
  preview.value = null;
}
function cursor() {
  if (textArea.value && preview.value)
    splitAt.value =
      preview.value.start +
      Array.from(textArea.value.value.slice(0, textArea.value.selectionStart))
        .length;
}
function move(delta: number) {
  if (!part.value || frozen.value) return;
  const i = parts.value.indexOf(part.value),
    j = i + delta;
  if (j >= 0 && j < parts.value.length)
    [parts.value[i], parts.value[j]] = [parts.value[j]!, parts.value[i]!];
}
function chooseTarget(match: ManuscriptMatch) {
  if (!part.value || frozen.value || finished(part.value.id)) return;
  part.value.mode = "replace";
  part.value.target = {
    control_id: match.control_id,
    revision: match.revision,
    hash: match.hash,
    base_head: match.base_head,
  };
  confirmReplace.value = false;
}
function modeChanged() {
  if (part.value && part.value.mode !== "replace") delete part.value.target;
}
function receive(p: Pending, value: unknown) {
  if (p.kind === "upload" || p.kind === "reparse") {
    const v = value as ManuscriptSource;
    if (!v.id || !Array.isArray(v.notes))
      throw new Error("原稿回执不完整，请核对。");
    sources.value = [v, ...sources.value.filter((s) => s.id !== v.id)];
    sourceId.value = v.id;
    selectedFile.value = null;
    notice.value =
      v.characters > 0
        ? "原稿已保留，请选择“分章预览”继续。"
        : "原稿已保留；未得到可导入文本，请检查格式后重新解析。";
  } else if (p.kind === "apply") {
    const v = value as ManuscriptProgress;
    if (
      v.batch_id !== p.batchId ||
      v.part_id !== p.partId ||
      !["done", "skipped", "failed"].includes(v.status)
    )
      throw new Error("分章回执不匹配，请核对。");
    if (batch.value?.id === p.batchId) {
      batch.value.progress = [
        ...batch.value.progress.filter((x) => x.part_id !== v.part_id),
        v,
      ];
      notice.value = v.status === "failed" ? v.reason : "本章进度已保存。";
    }
  } else acceptBatch(value as ManuscriptBatch);
}
async function send(p: Pending): Promise<boolean> {
  if (busy.value) return false;
  busy.value = true;
  pending.value = p;
  error.value = "";
  const auth = epoch;
  const accepts = () =>
    !disposed && auth === epoch && pending.value?.op === p.op;
  let succeeded = false;
  const work = (async () => {
    try {
      let v: unknown;
      if (p.file) {
        const form = new FormData();
        form.append("file", p.file);
        const response = await fetch(`/api${p.path}`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Idempotency-Key": p.op },
          body: form,
        });
        const body = await response.json();
        if (!response.ok)
          throw new APIError(body.error || "原稿上传未完成", response.status);
        v = body;
      } else v = await api(p.path, p.method, p.body);
      if (accepts()) {
        receive(p, v);
        pending.value = null;
        succeeded = true;
      }
    } catch (e) {
      if (!accepts()) return;
      if (
        e instanceof APIError &&
        [400, 401, 403, 404, 409, 413, 422].includes(e.status)
      )
        pending.value = null;
      fail(e);
    }
  })();
  let timer: ReturnType<typeof setTimeout> | undefined;
  await Promise.race([
    work,
    new Promise<void>((resolve) => {
      timer = setTimeout(() => {
        if (accepts())
          notice.value =
            "本次结果仍待核对，原请求没有取消或重复发送。请先核对结果。";
        resolve();
      }, 15000);
    }),
  ]);
  clearTimeout(timer);
  if (!disposed) busy.value = false;
  return succeeded;
}
async function upload() {
  if (locked.value || !selectedFile.value) return;
  const op = newOperationID();
  await send({
    kind: "upload",
    op,
    path: root + "/sources",
    method: "POST",
    body: {},
    file: selectedFile.value,
  });
}
async function reparse() {
  if (locked.value || !sourceId.value) return;
  const op = newOperationID();
  await send({
    kind: "reparse",
    op,
    path: `${root}/sources/${sourceId.value}/reparse`,
    method: "POST",
    body: { operation_id: op },
  });
}
async function create() {
  if (locked.value || !sourceId.value || dirty.value) return;
  const op = newOperationID();
  if (
    await send({
      kind: "create",
      op,
      path: root,
      method: "POST",
      body: { source_id: sourceId.value, operation_id: op },
      sourceId: sourceId.value,
    })
  )
    void loadPreview();
}
async function save() {
  if (!batch.value || frozen.value || !dirty.value) return;
  const op = newOperationID();
  await send({
    kind: "layout",
    op,
    path: `${root}/${batch.value.id}`,
    method: "PUT",
    body: {
      revision: batch.value.revision,
      hash: batch.value.hash,
      parts: cloneParts(parts.value),
      accept_partial: acceptPartial.value,
      operation_id: op,
    },
    batchId: batch.value.id,
  });
}
async function applyPart(p: ManuscriptPart) {
  const b = batch.value;
  if (
    !b ||
    dirty.value ||
    pending.value ||
    busy.value ||
    !props.canEdit ||
    b.document.recovered ||
    finished(p.id)
  )
    return false;
  const op = newOperationID();
  return send({
    kind: "apply",
    op,
    path: `${root}/${b.id}/parts/${p.id}/apply`,
    method: "POST",
    body: {
      revision: b.revision,
      hash: b.hash,
      progress_revision: progress.value.get(p.id)?.revision ?? 0,
      operation_id: op,
      allow_duplicate: allowDuplicate.value,
      confirm_replace: confirmReplace.value,
    },
    batchId: b.id,
    partId: p.id,
  });
}
async function run() {
  if (frozen.value || dirty.value || !batch.value) return;
  running.value = true;
  stopRequested.value = false;
  error.value = "";
  try {
    for (const p of parts.value) {
      if (disposed || stopRequested.value || pending.value) break;
      if (finished(p.id)) continue;
      selected.value = p.id;
      if (
        !(await applyPart(p)) ||
        progress.value.get(p.id)?.status === "failed"
      )
        break;
    }
  } finally {
    if (!disposed) {
      running.value = false;
      stopRequested.value = false;
      void loadPreview();
    }
  }
}
async function check() {
  const p = pending.value,
    auth = epoch;
  if (!p || busy.value || checking.value) return;
  checking.value = true;
  error.value = "";
  try {
    let value: unknown = null;
    if (p.kind === "upload" || p.kind === "reparse") {
      value = (
        await reads.read<{ source: ManuscriptSource | null }>(
          `${root}/sources/receipt?operation_id=${encodeURIComponent(p.op)}`,
        )
      ).source;
    } else if (p.kind === "create") {
      value = (
        await reads.read<{ batch: ManuscriptBatch | null }>(
          `${root}/by-source/${p.sourceId}`,
        )
      ).batch;
    } else {
      const v = await reads.read<ManuscriptBatch>(`${root}/${p.batchId}`);
      value =
        p.kind === "layout"
          ? v.document.operation_id === p.op
            ? v
            : null
          : (v.progress.find(
              (x) => x.part_id === p.partId && x.operation_id === p.op,
            ) ?? null);
    }
    if (disposed || auth !== epoch || pending.value?.op !== p.op) return;
    if (value) {
      receive(p, value);
      pending.value = null;
      notice.value = "已核对并恢复已保存的导入结果。";
    } else
      notice.value =
        "暂未读到本次结果，原请求仍可能完成。可以继续核对，或手动重试同一请求；不会自动重复导入。";
  } catch (e) {
    if (!disposed && auth === epoch) fail(e);
  } finally {
    if (!disposed) checking.value = false;
  }
}
function close() {
  if (blockLeave.value) return;
  emit("close");
}
function beforeUnload(e: BeforeUnloadEvent) {
  if (blockLeave.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", beforeUnload);
  await list();
  if (props.initialBatchId) await open(props.initialBatchId);
});
onBeforeUnmount(() => {
  disposed = true;
  epoch++;
  stopRequested.value = true;
  window.removeEventListener("beforeunload", beforeUnload);
});
</script>

<template>
  <section class="manuscript-desk" aria-label="长稿分章导入">
    <header class="section-heading">
      <div>
        <p class="eyebrow">已有原稿继续创作</p>
        <h1>长稿分章导入</h1>
        <p>保留原文件，先核对分章，再存入可编辑的章节私稿。导入不调用模型。</p>
      </div>
      <button :disabled="blockLeave" @click="close">返回写作台</button>
    </header>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <section v-if="pending" class="panel" aria-label="待核对的导入请求">
      <p>本次操作结果尚未确认。先核对，避免重复写入。</p>
      <div class="actions">
        <button :disabled="busy || checking" @click="check">
          {{ checking ? "正在核对…" : "核对结果" }}</button
        ><button :disabled="busy || checking" @click="send(pending)">
          手动重试同一请求</button
        ><button
          :disabled="busy || checking"
          @click="
            pending = null;
            notice =
              '已停止等待回执，原请求仍可能完成；稍后重新打开导入记录核对。';
          "
        >
          停止等待回执
        </button>
      </div>
    </section>
    <section class="panel">
      <h2>1. 选择原稿</h2>
      <p>
        UTF-8
        TXT/Markdown、DOCX优先；原文件最多32MB，已解析文本最多8MB。PDF及图片复用资料解析，缺页或未识别内容会明确提示。
      </p>
      <div class="actions">
        <input
          aria-label="选择长稿文件"
          type="file"
          accept=".txt,.md,.docx,.pdf,.png,.jpg,.jpeg,.webp"
          :disabled="locked || dirty"
          @change="
            selectedFile =
              ($event.target as HTMLInputElement).files?.[0] ?? null
          "
        /><button :disabled="locked || dirty || !selectedFile" @click="upload">
          上传并保留原稿</button
        ><button
          :disabled="loading || busy || dirty || !!pending"
          @click="list()"
        >
          刷新导入记录
        </button>
      </div>
      <label
        >或复用已上传资料<select v-model="sourceId" :disabled="locked || dirty">
          <option value="">选择原稿</option>
          <option v-for="s in sources" :key="s.id" :value="s.id">
            {{ s.name }} · {{ s.characters.toLocaleString() }}字{{
              s.complete ? "" : " · 解析待核对"
            }}
          </option>
        </select></label
      >
      <div class="actions">
        <button :disabled="locked || dirty || !sourceId" @click="create">
          分章预览 / 继续该原稿</button
        ><button :disabled="locked || dirty || !sourceId" @click="reparse">
          重新解析所选原稿</button
        ><a v-if="sourceId || batch" :href="originalURL" download>下载原文件</a
        ><button
          v-if="sourceAfter"
          :disabled="loading || dirty || !!pending"
          @click="list(false, true)"
        >
          更多资料
        </button>
      </div>
      <p v-for="note in source?.notes" :key="note">{{ note }}</p>
    </section>
    <details class="panel">
      <summary>已保存的导入记录（{{ batches.length }}）</summary>
      <ul>
        <li v-for="b in batches" :key="b.id">
          <button :disabled="blockLeave || loading" @click="open(b.id)">
            {{ new Date(b.document.created_at).toLocaleString() }} ·
            {{ b.document.total.toLocaleString() }}字 · 打开并续导
          </button>
        </li>
      </ul>
      <button
        v-if="batchAfter"
        :disabled="loading || blockLeave"
        @click="list(true)"
      >
        更多导入记录
      </button>
      <p v-if="!batches.length">还没有已保存的分章方案。</p>
    </details>
    <template v-if="batch">
      <section class="panel">
        <h2>2. 核对分章</h2>
        <a
          :href="`/api/projects/${book.project_id}/branches/${book.branch_id}/imports/${batch.document.source_id}/file`"
          download
          >下载本次导入的原文件</a
        >
        <p>
          {{ parts.length }}段 · 已完成{{
            doneCount
          }}段。标题、卷名和顺序可以调整；原文中的标题和空行照原样保留。已完成的分章不能重新切分。
        </p>
        <p v-if="batch.document.recovered" role="status">
          这是项目恢复副本，导入记录保留供核对。请重新选择原稿建立新导入；不会自动重复写入。
        </p>
        <label v-if="!batch.document.complete"
          ><input
            v-model="acceptPartial"
            type="checkbox"
            :disabled="frozen"
          />我已核对解析遗漏，仅导入已识别文本；原文件仍保留</label
        >
        <div class="actions">
          <button :disabled="frozen || !dirty" @click="save">
            保存分章方案</button
          ><button :disabled="frozen || !dirty" @click="resetLayout()">
            撤销未保存调整</button
          ><span v-if="dirty">分章调整尚未保存，先保存再预览或导入。</span>
        </div>
      </section>
      <div class="manuscript-columns">
        <aside class="panel part-list" aria-label="分章列表">
          <button
            v-for="(p, i) in parts"
            :key="p.id"
            :aria-current="selected === p.id ? 'true' : undefined"
            :disabled="busy || running || !!pending"
            @click="selectPart(p.id)"
          >
            <span>{{ i + 1 }}. {{ p.title }}</span
            ><small
              >{{ p.end - p.start }}字 ·
              {{
                progress.get(p.id)?.status === "done"
                  ? "已导入"
                  : progress.get(p.id)?.status === "skipped"
                    ? "已跳过"
                    : progress.get(p.id)?.status === "failed"
                      ? "待处理"
                      : p.mode === "skip"
                        ? "将跳过"
                        : p.mode === "replace"
                          ? "将替换"
                          : "待导入"
              }}</small
            >
          </button>
        </aside>
        <section class="panel part-detail" v-if="part">
          <h3>分章详情</h3>
          <label
            >章节标题<input
              v-model="part.title"
              :disabled="frozen || finished(part.id)"
              maxlength="600" /></label
          ><label
            >卷名<input
              v-model="part.volume"
              :disabled="frozen || finished(part.id)"
              maxlength="240" /></label
          ><label
            >处理方式<select
              v-model="part.mode"
              :disabled="frozen || finished(part.id)"
              @change="modeChanged"
            >
              <option value="new">另存新章</option>
              <option value="skip">跳过本段（仍保留原稿）</option>
              <option v-if="part.target" value="replace">
                替换已选择的章稿
              </option>
            </select></label
          >
          <div class="actions">
            <button :disabled="frozen" @click="move(-1)">上移</button
            ><button :disabled="frozen" @click="move(1)">下移</button
            ><button :disabled="frozen || finished(part.id)" @click="mergeNext">
              合并后续原文</button
            ><button
              :disabled="dirty || busy || reading"
              @click="loadPreview()"
            >
              {{ reading ? "读取中…" : "读取正文与重复核对" }}
            </button>
          </div>
          <p>
            原文位置 {{ part.start }}—{{ part.end }}，{{
              part.end - part.start
            }}字。
          </p>
          <template v-if="preview">
            <textarea
              ref="textArea"
              aria-label="分章原文预览"
              :value="preview.text"
              readonly
              rows="14"
              @click="cursor"
              @keyup="cursor"
            />
            <p>
              当前显示原文 {{ preview.start }}—{{
                preview.end
              }}。选中分章处或直接填写原文位置后，可以拆分。
            </p>
            <div class="actions">
              <button
                v-if="preview.next"
                :disabled="reading || dirty"
                @click="loadPreview(preview.next)"
              >
                下一段预览</button
              ><label
                >分章位置<input
                  v-model.number="splitAt"
                  type="number"
                  :min="part.start + 1"
                  :max="part.end - 1"
                  :disabled="frozen || finished(part.id)" /></label
              ><button
                :disabled="frozen || finished(part.id) || splitAt === null"
                @click="split"
              >
                在此分为两章
              </button>
            </div>
            <p v-for="note in preview.notes" :key="note">{{ note }}</p>
            <section aria-label="重复章节核对">
              <h4>现存章稿核对</h4>
              <p v-if="!preview.matches.length">
                本次核对未发现同名或完全相同正文{{
                  preview.uncertain ? "；部分来源待核对" : "。"
                }}
              </p>
              <div
                v-for="m in preview.matches"
                :key="m.control_id"
                class="match-row"
              >
                <strong
                  >{{ m.exact ? "相同正文" : "同名章节" }}：{{
                    m.title
                  }}</strong
                ><button
                  :disabled="frozen || finished(part.id)"
                  @click="chooseTarget(m)"
                >
                  选择替换此稿</button
                ><button
                  :disabled="blockLeave"
                  @click="emit('edit', m.chapter_id)"
                >
                  打开原章核对
                </button>
              </div>
              <p v-if="part.target">
                已选择替换目标：{{
                  part.target.control_id
                }}。保存分章后，导入前会再次核对版本并留存旧稿。
              </p>
            </section></template
          >
          <p v-if="progress.get(part.id)?.reason" role="status">
            {{ progress.get(part.id)?.reason }}
          </p>
          <label v-if="progress.get(part.id)?.chapter_id"
            >已有章节编号（可加入规划草稿）<input
              :value="progress.get(part.id)!.chapter_id"
              readonly
              @focus="($event.target as HTMLInputElement).select()"
          /></label>
          <button
            v-if="progress.get(part.id)?.chapter_id"
            :disabled="blockLeave"
            @click="emit('edit', progress.get(part.id)!.chapter_id)"
          >
            打开已导入章节
          </button>
        </section>
      </div>
      <section class="panel">
        <h2>3. 导入与续导</h2>
        <p>
          每章完成后保存进度。页面关闭或服务重启后，重新打开该记录可继续；离开页面后不会继续导入。导入稿可手动精修，AI续写和发布仍需先确认规划。
        </p>
        <label
          ><input
            v-model="allowDuplicate"
            type="checkbox"
            :disabled="frozen"
          />发现相同正文或来源待核对时，仍明确另存新章</label
        ><label
          ><input
            v-model="confirmReplace"
            type="checkbox"
            :disabled="frozen"
          />明确替换分章方案中已选目标；替换前保留旧稿</label
        >
        <div class="actions">
          <button
            :disabled="frozen || dirty || !part || finished(part.id)"
            @click="part && applyPart(part)"
          >
            导入 / 重试本段</button
          ><button
            :disabled="frozen || dirty || doneCount === parts.length"
            @click="run"
          >
            从未完成处继续导入</button
          ><button
            v-if="running"
            :disabled="stopRequested"
            @click="stopRequested = true"
          >
            {{ stopRequested ? "本章完成后停止" : "本章后停止" }}</button
          ><span v-if="running"
            >正在逐章导入：{{ doneCount }} / {{ parts.length }}</span
          >
        </div>
      </section>
    </template>
  </section>
</template>

<style scoped>
.manuscript-desk {
  display: grid;
  gap: 1rem;
}
.manuscript-columns {
  display: grid;
  grid-template-columns: minmax(13rem, 1fr) minmax(0, 3fr);
  gap: 1rem;
  align-items: start;
}
.part-list {
  max-height: 48rem;
  overflow: auto;
  display: grid;
  gap: 0.4rem;
}
.part-list button {
  text-align: left;
  display: grid;
  gap: 0.3rem;
  overflow-wrap: anywhere;
}
.part-list button[aria-current="true"] {
  outline: 2px solid var(--accent, #a66c35);
}
.part-detail {
  display: grid;
  gap: 0.8rem;
  min-width: 0;
}
.part-detail textarea {
  width: 100%;
  box-sizing: border-box;
  font: inherit;
  line-height: 1.8;
  white-space: pre-wrap;
}
.manuscript-desk label {
  display: grid;
  gap: 0.4rem;
}
.match-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.6rem;
  margin: 0.6rem 0;
}
.manuscript-desk select,
.manuscript-desk input {
  max-width: 100%;
  box-sizing: border-box;
}
.manuscript-desk small {
  font-weight: normal;
}
.manuscript-desk label:has(input[type="checkbox"]) {
  display: flex;
  align-items: baseline;
}
.manuscript-desk input[type="checkbox"] {
  width: auto;
}
@media (max-width: 760px) {
  .manuscript-columns {
    grid-template-columns: 1fr;
  }
  .part-list {
    max-height: 18rem;
  }
  .section-heading {
    flex-wrap: wrap;
  }
  .actions {
    flex-wrap: wrap;
  }
}
</style>
