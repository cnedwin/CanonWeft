<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { APIError, message } from "../../api";
import {
  initialProseSearchView,
  useProseSearchReads,
  type ProseSearchView,
} from "./proseSearchReads";
import type {
  ProseSearchItem,
  ProseSearchMatch,
  ProseSearchEditTarget,
} from "./proseSearchTypes";
const props = defineProps<{ bookId: string; initialView?: ProseSearchView }>();
const emit = defineEmits<{
  close: [];
  expired: [];
  view: [value: ProseSearchView];
  edit: [value: ProseSearchEditTarget];
}>();
const view = ref<ProseSearchView>({
  ...initialProseSearchView(),
  ...props.initialView,
});
const items = ref<ProseSearchItem[]>([]),
  cursor = ref(""),
  pages = ref(0),
  scanned = ref(0),
  busy = ref(false),
  capped = ref(false),
  error = ref(""),
  notice = ref("");
const notes = ref<string[]>([]),
  rowPage = ref(1);
const reads = useProseSearchReads();
let sequence = 0,
  loadedBytes = 0;
const validQuery = computed(
  () => !!view.value.query.trim() && Array.from(view.value.query).length <= 120,
);
const unapplied = computed(
  () =>
    !!view.value.appliedQuery && view.value.query !== view.value.appliedQuery,
);
const results = computed(() =>
  items.value.flatMap((item) => item.matches.map((hit) => ({ item, hit }))),
);
const rowPages = computed(() =>
  Math.max(1, Math.ceil(results.value.length / 50)),
);
const visible = computed(() =>
  results.value.slice((rowPage.value - 1) * 50, rowPage.value * 50),
);
const matched = computed(
  () => items.value.filter((item) => item.total_matches > 0).length,
);
const total = computed(() =>
  items.value.reduce((sum, item) => sum + item.total_matches, 0),
);
const limited = computed(() =>
  items.value.filter((item) => item.reason || item.truncated),
);
function clear() {
  items.value = [];
  cursor.value = "";
  pages.value = 0;
  scanned.value = 0;
  loadedBytes = 0;
  rowPage.value = 1;
  capped.value = false;
  notes.value = [];
}
function stop() {
  ++sequence;
  reads.cancel();
  busy.value = false;
  notice.value = "已停止读取；迟到结果不会加入，也不会继续下一页。";
}
async function load(mode: "new" | "refresh" | "more" | "all", wanted = 1) {
  if (!reads.active() || (busy.value && (mode === "all" || mode === "more")))
    return;
  const query = mode === "new" ? view.value.query : view.value.appliedQuery;
  if (!query.trim() || Array.from(query).length > 120) return;
  const current = ++sequence,
    book = props.bookId,
    source = view.value.source;
  reads.cancel();
  busy.value = true;
  error.value = "";
  notice.value = "";
  let after =
    mode === "new"
      ? ""
      : mode === "refresh"
        ? view.value.startCursor
        : cursor.value;
  let reset = mode === "new" || mode === "refresh";
  const requests = mode === "all" ? 200 : Math.max(1, Math.min(200, wanted));
  const seen = new Set<string>();
  try {
    for (let count = 0; count < requests; count++) {
      if (current !== sequence || !reads.active()) return;
      const page = await reads.read(book, source, query, after);
      if (current !== sequence || !reads.active()) return;
      if (
        page.next_cursor &&
        (page.next_cursor === after || seen.has(page.next_cursor))
      )
        throw new Error("正文分页没有推进，请重新查找。");
      seen.add(after);
      const bytes = new TextEncoder().encode(JSON.stringify(page)).length;
      if (
        !reset &&
        (items.value.length + page.items.length > 1000 ||
          loadedBytes + bytes > 8 * 1048576)
      ) {
        capped.value = true;
        break;
      }
      if (reset) {
        clear();
        if (mode === "new") {
          view.value.startCursor = "";
          view.value.focusedControl = "";
          view.value.focusedHash = "";
          view.value.focusedStart = -1;
        }
        view.value.appliedQuery = query;
        reset = false;
      }
      const known = new Set(items.value.map((item) => item.control_id));
      if (page.items.some((item) => known.has(item.control_id)))
        throw new Error("章稿分页重复，请刷新后核对覆盖范围。");
      items.value.push(...page.items);
      scanned.value += page.scanned;
      pages.value++;
      loadedBytes += bytes;
      cursor.value = page.next_cursor;
      after = cursor.value;
      notes.value = page.notes;
      view.value.pages = pages.value;
      if (!after) break;
      if (pages.value >= 200 || items.value.length >= 1000) {
        capped.value = true;
        break;
      }
    }
    const focused = items.value.find(
      (item) => item.control_id === view.value.focusedControl,
    );
    const focusedIndex = results.value.findIndex(
      ({ item, hit }) =>
        item.control_id === view.value.focusedControl &&
        item.source_hash === view.value.focusedHash &&
        hit.start === view.value.focusedStart,
    );
    if (focusedIndex >= 0) rowPage.value = Math.floor(focusedIndex / 50) + 1;
    if (
      focused &&
      view.value.focusedHash &&
      (focused.source_hash !== view.value.focusedHash ||
        !focused.matches.some((hit) => hit.start === view.value.focusedStart))
    )
      notice.value =
        "原匹配所在章稿已变化或该位置不再匹配，已重查当前保存稿，请重新选择；未把原问题自动标为解决。";
    else if (!focused && view.value.focusedControl)
      notice.value =
        "本次已读取范围未包含原匹配章稿，请核对来源与后续页；不能据此判断问题已解决。";
  } catch (e) {
    if (current !== sequence || !reads.active()) return;
    error.value = message(e);
    if (e instanceof APIError && [401, 403].includes(e.status)) {
      clear();
      view.value.focusedControl = "";
      view.value.focusedHash = "";
      view.value.focusedStart = -1;
      if (e.status === 401) emit("expired");
    } else if (items.value.length)
      notice.value = "保留上次已读取结果，尚未完成此次重查，请核对来源与覆盖。";
  } finally {
    if (current === sequence && reads.active()) busy.value = false;
  }
}
function nextWindow() {
  if (busy.value || !cursor.value) return;
  view.value.startCursor = cursor.value;
  view.value.focusedControl = "";
  view.value.focusedHash = "";
  view.value.focusedStart = -1;
  void load("refresh");
}
function open(item: ProseSearchItem, hit: ProseSearchMatch) {
  if (busy.value || item.origin === "unavailable") return;
  view.value.focusedControl = item.control_id;
  view.value.focusedHash = item.source_hash;
  view.value.focusedStart = hit.start;
  // The edit event unmounts this view before its scheduled deep watcher runs.
  emit("view", { ...view.value });
  emit("edit", {
    chapter_id: item.chapter_id,
    control_id: item.control_id,
    quote: hit.quote,
    reviewed_hash: item.source_hash,
    match_start: hit.start,
    match_end: hit.end,
    title: `正文查找：${view.value.appliedQuery}`,
    explanation: `${item.origin === "private" ? "本人已保存私人稿" : "正式采用稿"}；原文位置 ${hit.start}–${hit.end}。精确文字匹配不代表人物身份或语义影响。`,
    suggestion:
      "结合上下文核对称谓、细节和前后衔接，按需要手动修改或准备局部精修；不自动替换全书，不把同名人物或代词当成同一身份。",
  });
}
watch(view, (value) => emit("view", { ...value }), { deep: true });
watch(
  rowPages,
  (count) => (rowPage.value = Math.max(1, Math.min(rowPage.value, count))),
);
watch(
  () => view.value.source,
  () => {
    ++sequence;
    reads.cancel();
    busy.value = false;
    clear();
    error.value = "";
    view.value.appliedQuery = "";
    view.value.startCursor = "";
    view.value.pages = 1;
    view.value.focusedControl = "";
    view.value.focusedHash = "";
    view.value.focusedStart = -1;
    notice.value = "来源已切换，请开始查找。";
  },
  { flush: "sync" },
);
watch(
  () => props.bookId,
  () => {
    ++sequence;
    reads.cancel();
    busy.value = false;
    clear();
    view.value = initialProseSearchView();
    error.value = "";
    notice.value = "";
  },
  { flush: "sync" },
);
onMounted(() => {
  if (view.value.appliedQuery) void load("refresh", view.value.pages);
});
</script>

<template>
  <section class="prose-search" aria-label="全书正文查找">
    <header class="actions">
      <h1>全书正文查找</h1>
      <button @click="emit('close')">返回小说</button>
    </header>
    <p>
      查找已保存正文中的精确文字，区分大小写，不使用正则表达式。可排查名字、称谓和细节，再逐处修改。不会检索代词含义、推断同名身份或自动替换全书。
    </p>
    <form class="panel" @submit.prevent="load('new')">
      <label
        >查找正文文字<textarea
          v-model="view.query"
          aria-label="查找正文文字"
          rows="2"
          placeholder="输入要查找的原文，最多120个字符"
        />
      </label>
      <label
        >正文来源<select v-model="view.source" aria-label="正文来源">
          <option value="working">本人工作稿（没有私人稿时使用正式稿）</option>
          <option value="published">正式采用稿</option>
        </select></label
      >
      <button :disabled="busy || !validQuery" type="submit">开始查找</button>
      <p v-if="!validQuery && view.query">请输入1—120个字符，不能全为空白。</p>
    </form>
    <p class="muted">
      仅扫描已有章节工作稿；未创建或未保存的正文不在范围内。包括仍留在本书中的旧章稿，章节顺序变化不以书名或同名标题合并。未保存的本页编辑不在结果中。
    </p>
    <p v-if="busy">正在查找已保存原文…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice || unapplied" role="status">
      {{ notice }}
      {{ unapplied ? "输入已修改但尚未应用；下方仍是原查询结果。" : "" }}
    </p>
    <template v-if="view.appliedQuery && pages">
      <p aria-label="正文查找覆盖">
        当前结果查询：{{ view.appliedQuery }}。已扫描 {{ scanned }} 章 · 命中
        {{ matched }} 章 · {{ total }} 处 · 已加载
        {{ results.length }} 个位置。{{
          cursor ? "仍有章节未扫描。" : "本批已扫描至末尾。"
        }}
      </p>
      <div class="actions">
        <button :disabled="busy" @click="load('refresh', view.pages)">
          刷新当前结果</button
        ><button :disabled="busy || !cursor || capped" @click="load('more')">
          继续读取章节</button
        ><button :disabled="busy || !cursor || capped" @click="load('all')">
          读取其余章节</button
        ><button v-if="busy" @click="stop">停止读取</button>
      </div>
      <p v-if="capped" role="status">
        本批已到容量上限，可保留查询并清空显示后继续下一批。
      </p>
      <button v-if="capped && cursor" :disabled="busy" @click="nextWindow">
        清空显示并继续下一批
      </button>
      <ul v-if="notes.length" class="muted">
        <li v-for="(note, index) in notes" :key="index">{{ note }}</li>
      </ul>
      <details v-if="limited.length" open>
        <summary>覆盖限制与待核对章稿 {{ limited.length }}</summary>
        <ul>
          <li v-for="item in limited" :key="item.control_id">
            {{ item.chapter_title || item.chapter_id || item.control_id }}：{{
              item.reason
            }}
            {{
              item.truncated
                ? `本章共${item.total_matches}处，仅展示前${item.matches.length}处；请缩小查找文字。`
                : ""
            }}
          </li>
        </ul>
      </details>
      <p v-if="!results.length">
        已扫描范围内没有可展示的匹配；请同时核对未读章节和来源限制，不能据此判断全书无影响。
      </p>
      <nav v-if="rowPages > 1" aria-label="正文匹配分页">
        <button :disabled="rowPage <= 1" @click="rowPage--">上一页</button
        ><span>第 {{ rowPage }} / {{ rowPages }} 页</span
        ><button :disabled="rowPage >= rowPages" @click="rowPage++">
          下一页
        </button>
      </nav>
      <article
        v-for="{ item, hit } in visible"
        :key="`${item.control_id}:${item.source_hash}:${hit.start}`"
        class="panel"
        :class="{
          focused:
            item.control_id === view.focusedControl &&
            hit.start === view.focusedStart,
        }"
      >
        <h2>
          {{ item.chapter_title }}
          <small>{{
            item.origin === "private" ? "本人私人稿" : "正式采用稿"
          }}</small>
        </h2>
        <p class="muted">
          章节 {{ item.chapter_id }} · 原文位置 {{ hit.start }}–{{
            hit.end
          }}（Unicode字符）
        </p>
        <blockquote>
          {{ hit.before }}<mark>{{ hit.quote }}</mark
          >{{ hit.after }}
        </blockquote>
        <button
          :disabled="busy"
          :aria-label="`打开匹配 ${item.control_id} ${hit.start}`"
          @click="open(item, hit)"
        >
          定位原文并打开章稿
        </button>
      </article>
    </template>
    <button v-if="busy && !pages" @click="stop">停止读取</button>
  </section>
</template>
<style scoped>
.prose-search {
  display: grid;
  gap: 0.8rem;
}
.actions {
  display: flex;
  gap: 0.7rem;
  align-items: center;
  flex-wrap: wrap;
}
h1,
h2,
p {
  margin: 0;
}
h2 {
  font-size: 1.05rem;
}
label {
  display: grid;
  gap: 0.3rem;
}
.panel {
  display: grid;
  gap: 0.7rem;
}
blockquote {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  margin: 0;
  padding: 0.8rem;
  background: #f5f4ef;
}
mark {
  background: #ffe79a;
  color: #242424;
}
.focused {
  outline: 2px solid #8c6534;
}
small {
  font-weight: normal;
}
nav {
  display: flex;
  gap: 0.8rem;
  align-items: center;
}
</style>
