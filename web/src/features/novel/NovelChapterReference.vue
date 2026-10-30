<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { APIError, message } from "../../api";
import { useChapterReferenceReads } from "./useChapterReferenceReads";
import {
  referenceExcerpt,
  referenceInstruction,
  referenceKey,
  type ChapterReferenceDirectory,
  type ChapterReferenceSnapshot,
  type ReferenceExcerpt,
  type ReferenceSource,
} from "./chapterReference";

const props = defineProps<{
  bookId: string;
  chapterId: string;
  context: string;
  disabled: boolean;
}>();
const emit = defineEmits<{
  close: [];
  expired: [];
  instruction: [value: { context: string; instruction: string }];
}>();
const reads = useChapterReferenceReads();
const directory = ref<ChapterReferenceDirectory | null>(null),
  snapshot = ref<ChapterReferenceSnapshot | null>(null);
const selected = ref(""),
  exactId = ref(""),
  query = ref(""),
  source = ref<ReferenceSource>("working");
const loading = ref(false),
  reading = ref(false),
  checking = ref(false);
const directoryError = ref(""),
  error = ref(""),
  selectionError = ref("");
const excerpt = ref<ReferenceExcerpt | null>(null);
let listSequence = 0,
  sequence = 0;
const rows = computed(() => directory.value?.items ?? []);
const current = computed(() =>
  rows.value.findIndex((row) => row.chapter_id === props.chapterId),
);
const previous = computed(() =>
  directory.value?.complete && current.value > 0
    ? rows.value[current.value - 1]
    : undefined,
);
const next = computed(() =>
  directory.value?.complete && current.value >= 0
    ? rows.value[current.value + 1]
    : undefined,
);
const matches = computed(() =>
  rows.value.filter((row) =>
    `${row.title}\n${row.volume}\n${row.chapter_id}`
      .toLocaleLowerCase()
      .includes(query.value.trim().toLocaleLowerCase()),
  ),
);
const chosen = computed(() => exactId.value.trim() || selected.value);
function invalidate() {
  ++sequence;
  snapshot.value = null;
  excerpt.value = null;
  reading.value = false;
  checking.value = false;
  error.value = "";
  selectionError.value = "";
}
function fail(cause: unknown) {
  error.value = message(cause);
  if (cause instanceof APIError && [401, 403].includes(cause.status)) {
    ++listSequence;
    directory.value = null;
    loading.value = false;
    directoryError.value = message(cause);
    selected.value = "";
    exactId.value = "";
    if (cause.status === 401) emit("expired");
  }
}
async function refreshDirectory() {
  const request = ++listSequence,
    book = props.bookId;
  loading.value = true;
  directoryError.value = "";
  invalidate();
  try {
    const value = await reads.directory(book);
    if (!reads.active() || request !== listSequence || book !== props.bookId)
      return;
    directory.value = value;
    // Saved chapters can outlive planning entries. A late directory must not
    // erase the author's explicit reference choice.
  } catch (cause) {
    if (!reads.active() || request !== listSequence) return;
    directory.value = null;
    directoryError.value = message(cause);
    fail(cause);
  } finally {
    if (reads.active() && request === listSequence) loading.value = false;
  }
}
async function readChapter(id = chosen.value) {
  if (!/^[a-f0-9]{32}$/.test(id) || id === props.chapterId) {
    error.value = "请选择其他已有章节，或填写其准确章节编号。";
    return;
  }
  if (exactId.value.trim() !== id) exactId.value = "";
  selected.value = id;
  // Flush prop/input watchers before claiming this request's sequence.
  await Promise.resolve();
  invalidate();
  const request = sequence,
    book = props.bookId,
    origin = source.value;
  reading.value = true;
  try {
    const value = await reads.chapter(book, id, origin);
    if (!reads.active() || request !== sequence || book !== props.bookId)
      return;
    const row = rows.value.find((item) => item.chapter_id === id);
    if (row?.control_id && row.control_id !== value.control_id)
      throw new Error("参考章稿身份与目录不一致，请重新读取目录。");
    snapshot.value = value;
  } catch (cause) {
    if (reads.active() && request === sequence) fail(cause);
  } finally {
    if (reads.active() && request === sequence) reading.value = false;
  }
}
function select(event: Event) {
  excerpt.value = null;
  selectionError.value = "";
  const field = event.target as HTMLTextAreaElement;
  if (!snapshot.value || field.selectionStart === field.selectionEnd) return;
  try {
    excerpt.value = referenceExcerpt(
      snapshot.value,
      field.selectionStart,
      field.selectionEnd,
    );
  } catch (cause) {
    selectionError.value = message(cause);
  }
}
async function addInstruction() {
  if (!snapshot.value || !excerpt.value || props.disabled || checking.value)
    return;
  const original = snapshot.value,
    selectedExcerpt = { ...excerpt.value },
    key = JSON.stringify(excerpt.value);
  const context = props.context,
    request = sequence;
  checking.value = true;
  error.value = "";
  try {
    const fresh = await reads.chapter(
      original.book_id,
      original.chapter_id,
      original.source,
    );
    if (!reads.active() || request !== sequence) return;
    if (referenceKey(fresh) !== referenceKey(original)) {
      snapshot.value = fresh;
      excerpt.value = null;
      throw new Error(
        "参考章已变化，请根据新原文重新选择选段；原精修输入保持原样。",
      );
    }
    if (
      props.disabled ||
      context !== props.context ||
      key !== JSON.stringify(excerpt.value)
    )
      throw new Error("当前编辑或选段已变化，请重新确认；原输入保持原样。");
    emit("instruction", {
      context,
      instruction: referenceInstruction(fresh, selectedExcerpt),
    });
  } catch (cause) {
    if (!reads.active() || request !== sequence) return;
    if (cause instanceof APIError && [401, 403].includes(cause.status)) {
      snapshot.value = null;
      excerpt.value = null;
    }
    fail(cause);
  } finally {
    if (reads.active() && request === sequence) checking.value = false;
  }
}
watch([source, selected, exactId], invalidate, { flush: "sync" });
watch(
  () => [props.bookId, props.chapterId],
  () => {
    ++listSequence;
    invalidate();
    directory.value = null;
    selected.value = "";
    exactId.value = "";
    query.value = "";
    void refreshDirectory();
  },
  { immediate: true },
);
</script>

<template>
  <section class="panel chapter-reference" aria-label="跨章原文参照">
    <header class="reference-actions">
      <h2>跨章原文参照</h2>
      <button type="button" @click="emit('close')">收起参考</button>
    </header>
    <p class="muted">
      查看已有原文时，本章继续保留。这里不生成、不改稿，也不把作者能看到的后文当作角色已知事实。
    </p>
    <p v-if="loading" role="status">正在读取已有规划目录…</p>
    <p v-if="directoryError" role="alert">{{ directoryError }}</p>
    <div class="reference-actions">
      <button
        type="button"
        :disabled="loading || !previous?.control_id"
        @click="readChapter(previous?.chapter_id)"
      >
        参照上一章</button
      ><button
        type="button"
        :disabled="loading || !next?.control_id"
        @click="readChapter(next?.chapter_id)"
      >
        参照下一章</button
      ><button type="button" :disabled="loading" @click="refreshDirectory">
        刷新参考目录
      </button>
    </div>
    <details :open="!snapshot" class="reference-picker">
      <summary>选择参考章节与来源</summary>
      <label
        >筛选参考章节<input
          v-model="query"
          type="search"
          maxlength="200"
          placeholder="标题、分卷或章节编号"
      /></label>
      <label
        >参考章节<select
          v-model="selected"
          aria-label="参考章节"
          @change="exactId = ''"
        >
          <option value="">请选择章节</option>
          <option
            v-if="selected && !rows.some((row) => row.chapter_id === selected)"
            :value="selected"
          >
            目录外已有章 · {{ selected }}
          </option>
          <option
            v-for="row in matches.slice(0, 200)"
            :key="row.chapter_id"
            :value="row.chapter_id"
            :disabled="!row.control_id || row.chapter_id === chapterId"
          >
            {{ row.volume }} / {{ row.title
            }}{{ row.reason ? " · " + row.reason : "" }}
          </option>
        </select></label
      >
      <p class="muted">
        当前目录匹配 {{ matches.length }} 章{{
          matches.length > 200 ? "，显示前200章，请缩小筛选范围" : ""
        }}。{{
          directory && !directory.complete
            ? "目录尚不完整，前后章快捷按钮已停用。"
            : ""
        }}
      </p>
      <p
        v-for="(note, index) in directory?.notes ?? []"
        :key="index"
        class="muted"
      >
        {{ note }}
      </p>
      <details>
        <summary>按准确章节编号回查</summary>
        <label
          >准确章节编号<input
            v-model="exactId"
            maxlength="32"
            placeholder="已有章节的稳定编号"
        /></label>
        <p class="muted">
          可用于规划已移出的已有章节；未找到时保留提示，不创建新章。
        </p>
      </details>
      <label
        >参考正文来源<select v-model="source" aria-label="参考正文来源">
          <option value="working">当前作者已保存稿优先</option>
          <option value="published">仅已采用稿</option>
        </select></label
      >
      <button
        type="button"
        :disabled="reading || !chosen"
        @click="readChapter()"
      >
        {{ reading ? "正在读取原文…" : "读取参考原文" }}
      </button>
    </details>
    <p v-if="error" role="alert">{{ error }}</p>
    <template v-if="snapshot">
      <h3>{{ snapshot.title }}</h3>
      <p class="muted">参考章节编号：{{ snapshot.chapter_id }}</p>
      <p>
        {{
          snapshot.origin === "private" ? "当前作者已保存私人稿" : "已采用稿"
        }}
        · 修订 {{ snapshot.revision }} ·
        {{ snapshot.characters.toLocaleString() }} 字符
      </p>
      <p
        v-if="snapshot.origin === 'published' && snapshot.source === 'working'"
        class="muted"
      >
        本作者没有私人稿，当前使用已采用正文。
      </p>
      <p v-if="snapshot.reason" role="status">{{ snapshot.reason }}</p>
      <label
        >参考章节原文<textarea
          :value="snapshot.text"
          readonly
          rows="24"
          @select="select"
        />
      </label>
      <p class="muted">
        读取快照；如原章另有修改，请重新读取。选段最多1500字，加入精修前会只读重核来源。
      </p>
      <p v-if="selectionError" role="alert">{{ selectionError }}</p>
      <blockquote v-if="excerpt" aria-label="参考原文选段">
        {{ excerpt.quote }}
      </blockquote>
      <button
        type="button"
        :disabled="!excerpt || disabled || checking"
        @click="addInstruction"
      >
        {{ checking ? "正在核对参考来源…" : "将选段加入精修要求" }}
      </button>
      <p class="muted">
        来源摘要：{{
          snapshot.source_hash
        }}。补充参考不会改动正文、账本或记忆；生成仍需明确执行。
      </p>
    </template>
  </section>
</template>
<style scoped>
.chapter-reference {
  min-width: 0;
  overflow-wrap: anywhere;
}
.reference-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}
.reference-actions h2 {
  flex: 1;
  margin: 0;
}
textarea {
  width: 100%;
  min-width: 0;
  resize: vertical;
}
label {
  display: grid;
  gap: 0.35rem;
  margin: 0.7rem 0;
}
select,
input {
  min-width: 0;
  width: 100%;
}
blockquote {
  white-space: pre-wrap;
  margin: 0.7rem 0;
  max-height: 16rem;
  overflow: auto;
}
</style>
