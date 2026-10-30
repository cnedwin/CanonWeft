<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import type { ChapterDraft } from "./chapterTypes";
import type { OldDraftTake } from "./NovelChapterOldDraft.vue";
import NovelProseDiff from "./NovelProseDiff.vue";

interface HistoryMeta {
  id: string;
  chapter_id: string;
  source_revision: number;
  source_hash: string;
  source_base_head: string;
  text_hash: string;
  title: string;
  characters: number;
  kind: "auto" | "manual" | "before_restore" | "before_replace";
  note: string;
  created_at: string;
  operation_id?: string;
}
interface HistoryText extends HistoryMeta {
  text: string;
}
interface HistoryPage {
  items: HistoryMeta[];
  next: string;
}
interface Checkpoint {
  revision: number;
  hash: string;
  note: string;
  operation_id: string;
}
const props = defineProps<{
  bookId: string;
  current: ChapterDraft;
  saved: boolean;
  disabled?: boolean;
  undoAvailable?: boolean;
}>();
const emit = defineEmits<{
  close: [];
  expired: [];
  take: [value: OldDraftTake];
  undo: [];
  blocking: [value: boolean];
}>();
const reads = useBoundedReads({
  stopped: "已离开修订历史。",
  timeout: "修订历史读取超时，可以重试；当前手稿和备注仍保留。",
});
const root = computed(
  () =>
    `/novels/${encodeURIComponent(props.bookId)}/chapters/${encodeURIComponent(props.current.resource.id)}/history`,
);
const items = ref<HistoryMeta[]>([]),
  next = ref(""),
  selected = ref("");
const old = ref<HistoryText | null>(null),
  note = ref(""),
  error = ref(""),
  notice = ref("");
const loading = ref(false),
  reading = ref(false),
  creating = ref(false),
  checking = ref(false),
  acknowledged = ref(false);
const unknown = ref<Checkpoint | null>(null);
const blocking = computed(() => creating.value || !!unknown.value);
watch(blocking, (value) => emit("blocking", value), { flush: "sync" });
const currentKey = computed(() =>
  JSON.stringify([
    props.current.resource.id,
    props.current.revision,
    props.current.hash,
    props.current.document,
  ]),
);
const canTake = computed(
  () =>
    !!old.value &&
    old.value.id === selected.value &&
    old.value.chapter_id === props.current.document.chapter_id &&
    props.saved &&
    !props.disabled &&
    !blocking.value &&
    !reading.value &&
    acknowledged.value &&
    old.value.text !== props.current.document.text,
);
let listSequence = 0,
  textSequence = 0;
const labels = {
  auto: "自动留版",
  manual: "手动留版",
  before_restore: "取回前留版",
  before_replace: "替换前留版",
};
function fail(cause: unknown) {
  error.value = message(cause);
  if (cause instanceof APIError && [401, 403].includes(cause.status)) {
    ++listSequence;
    ++textSequence;
    items.value = [];
    old.value = null;
    selected.value = "";
    next.value = "";
    acknowledged.value = false;
    loading.value = false;
    reading.value = false;
    // The identity of an unknown write is kept for read-back. A denial is not
    // evidence that the earlier write did not commit.
    if (cause.status === 401) emit("expired");
  }
}
function valid(item: HistoryMeta) {
  return (
    item.chapter_id === props.current.document.chapter_id &&
    !!item.id &&
    !!item.source_hash &&
    !!item.text_hash &&
    Number.isInteger(item.source_revision) &&
    item.source_revision >= 0 &&
    Number.isInteger(item.characters) &&
    item.characters >= 0 &&
    item.characters <= 100000 &&
    !!labels[item.kind]
  );
}
async function load(more = false) {
  if (loading.value) return;
  const epoch = ++listSequence,
    path = root.value;
  loading.value = true;
  error.value = "";
  try {
    const page = await reads.read<HistoryPage>(
      path +
        (more && next.value ? `?after=${encodeURIComponent(next.value)}` : ""),
    );
    if (!reads.active() || epoch !== listSequence || path !== root.value)
      return;
    if (
      !Array.isArray(page.items) ||
      !page.items.every(valid) ||
      typeof page.next !== "string"
    )
      throw new Error("修订历史目录不完整，请重新读取。");
    const previous = more ? items.value : [];
    items.value = [
      ...new Map(
        [...previous, ...page.items].map((item) => [item.id, item]),
      ).values(),
    ];
    next.value = page.next;
    if (!items.value.some((item) => item.id === selected.value))
      selected.value = items.value[0]?.id ?? "";
  } catch (cause) {
    if (reads.active() && epoch === listSequence) fail(cause);
  } finally {
    if (reads.active() && epoch === listSequence) loading.value = false;
  }
}
async function readText() {
  if (!selected.value || reading.value) return;
  const epoch = ++textSequence,
    id = selected.value,
    path = root.value;
  reading.value = true;
  old.value = null;
  acknowledged.value = false;
  error.value = "";
  try {
    const value = await reads.read<HistoryText>(
      `${path}/${encodeURIComponent(id)}`,
    );
    if (
      !reads.active() ||
      epoch !== textSequence ||
      path !== root.value ||
      selected.value !== id
    )
      return;
    if (!valid(value) || value.id !== id || typeof value.text !== "string")
      throw new Error("历史正文身份不一致，请重新读取。");
    old.value = value;
  } catch (cause) {
    if (reads.active() && epoch === textSequence) fail(cause);
  } finally {
    if (reads.active() && epoch === textSequence) reading.value = false;
  }
}
function confirmReceipt(value: HistoryMeta, input: Checkpoint) {
  if (
    !valid(value) ||
    value.operation_id !== input.operation_id ||
    value.source_revision !== input.revision ||
    value.source_hash !== input.hash ||
    value.note !== input.note
  )
    throw new Error("留版回执与提交依据不一致，请只读核对结果。");
  unknown.value = null;
  notice.value = "版本已留存，当前正文与写作依据保持原样。";
  if (note.value === input.note) note.value = "";
  items.value = [value, ...items.value.filter((item) => item.id !== value.id)];
  selected.value = value.id;
}
async function checkpoint() {
  if (
    props.disabled ||
    !props.saved ||
    blocking.value ||
    Array.from(note.value).length > 120
  )
    return;
  const path = root.value,
    input: Checkpoint = {
      revision: props.current.revision,
      hash: props.current.hash,
      note: note.value,
      operation_id: newOperationID(),
    };
  creating.value = true;
  unknown.value = input;
  error.value = "";
  notice.value = "";
  try {
    const value = await api<HistoryMeta>(path, "POST", input);
    if (!reads.active() || root.value !== path) return;
    confirmReceipt(value, input);
  } catch (cause) {
    if (reads.active()) {
      // Explicit rejected requests have no checkpoint; lost/invalid responses may
      // have committed. Never repeat the POST based on a read deadline.
      if (
        cause instanceof APIError &&
        cause.status >= 400 &&
        cause.status < 500
      )
        unknown.value = null;
      fail(cause);
    }
  } finally {
    if (reads.active()) creating.value = false;
  }
}
async function check() {
  const input = unknown.value,
    path = root.value;
  if (!input || checking.value || creating.value) return;
  checking.value = true;
  error.value = "";
  try {
    const page = await reads.read<HistoryPage>(
      `${path}?operation_id=${encodeURIComponent(input.operation_id)}`,
    );
    if (!reads.active() || unknown.value !== input || root.value !== path)
      return;
    if (page.items.length === 1) confirmReceipt(page.items[0]!, input);
    else if (page.items.length === 0)
      notice.value =
        "暂未读到该次留版，原请求仍可能迟到；可以继续核对，未重发请求。";
    else throw new Error("留版结果不唯一，请保留当前手稿并重新核对。");
  } catch (cause) {
    if (reads.active() && unknown.value === input) fail(cause);
  } finally {
    if (reads.active()) checking.value = false;
  }
}
function take() {
  if (!canTake.value || !old.value) return;
  emit("take", {
    text: old.value.text,
    resourceId: props.current.resource.id,
    revision: props.current.revision,
    hash: props.current.hash,
    document: JSON.stringify(props.current.document),
  });
  acknowledged.value = false;
}
watch(
  selected,
  () => {
    ++textSequence;
    old.value = null;
    acknowledged.value = false;
    reading.value = false;
  },
  { flush: "sync" },
);
watch(
  currentKey,
  () => {
    acknowledged.value = false;
  },
  { flush: "sync" },
);
onMounted(() => load());
</script>
<template>
  <section class="panel chapter-history" aria-label="章节修订历史">
    <div class="actions">
      <h2>章节修订历史</h2>
      <button :disabled="blocking" @click="emit('close')">关闭修订历史</button>
    </div>
    <p>
      历史仅属于当前作者。日常保存最多每五分钟自动留一版；手动留版与取回、替换前留版单独保留。读取不会修改正文或启动模型。
    </p>
    <div class="actions">
      <label
        >版本备注<input
          v-model="note"
          :disabled="blocking"
          maxlength="240"
          placeholder="如：调整人物动机前（最多120字）"
      /></label>
      <button
        :disabled="
          disabled || !saved || blocking || Array.from(note).length > 120
        "
        @click="checkpoint"
      >
        {{ creating ? "正在留版…" : "保存当前版本" }}
      </button>
    </div>
    <p v-if="!saved">当前手稿尚未确认保存，保存完成后可以命名留版和取回。</p>
    <div v-if="unknown" class="panel" role="status">
      留版结果尚未确认。本页手稿保留，未重复提交；请先核对结果再离开。
      <button :disabled="creating || checking" @click="check">
        {{ checking ? "正在核对…" : "核对留版结果" }}
      </button>
      <button
        :disabled="creating || checking"
        @click="
          unknown = null;
          notice = '已放弃等待该次回执；不会取消原请求，稍后可刷新历史查看。';
        "
      >
        放弃等待留版回执
      </button>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <div class="actions">
      <label
        >历史版本<select
          v-model="selected"
          :disabled="reading"
          aria-label="历史版本"
        >
          <option value="">请选择版本</option>
          <option v-for="item in items" :key="item.id" :value="item.id">
            {{ item.created_at }} · {{ labels[item.kind] }} ·
            {{ item.note || item.title }} ·
            {{ item.characters.toLocaleString() }}字符
          </option>
        </select></label
      >
      <button :disabled="loading" @click="load()">
        {{ loading ? "正在读取…" : "刷新修订历史" }}
      </button>
      <button v-if="next" :disabled="loading" @click="load(true)">
        加载更多修订
      </button>
      <button :disabled="!selected || reading" @click="readText">
        {{ reading ? "正在读取…" : "读取所选版本" }}
      </button>
    </div>
    <p v-if="!loading && !items.length && !error">
      尚无章节历史。可以保存当前版本；新功能启用前未留存的修改不会被补造为历史。
    </p>
    <template v-if="old">
      <p>
        {{ old.title }} · {{ old.created_at }} · 保存修订{{
          old.source_revision
        }}。来源摘要：{{
          old.source_hash
        }}。旧写作依据只作为历史来源，不恢复执行或采用资格。
      </p>
      <NovelProseDiff
        :left="old.text"
        :right="current.document.text"
        left-label="历史正文"
        right-label="当前私人稿正文"
      />
      <label class="confirm"
        ><input
          v-model="acknowledged"
          type="checkbox"
          :disabled="
            disabled || !saved || blocking || old.text === current.document.text
          "
        />我确认用历史正文替换当前私人稿正文；保存时先留存替换前正文</label
      >
      <div class="actions">
        <button :disabled="!canTake" @click="take">取回这版正文</button
        ><button
          v-if="undoAvailable"
          :disabled="disabled || !saved || blocking"
          @click="emit('undo')"
        >
          撤销这次旧稿取回
        </button>
      </div>
    </template>
  </section>
</template>
<style scoped>
.chapter-history {
  overflow-wrap: anywhere;
  min-width: 0;
}
.chapter-history .actions label {
  flex: 1;
  min-width: min(18rem, 100%);
}
.chapter-history select,
.chapter-history input {
  max-width: 100%;
}
.chapter-history .confirm {
  display: flex;
  gap: 0.5rem;
  align-items: flex-start;
}
.chapter-history .confirm input {
  width: auto;
}
</style>
