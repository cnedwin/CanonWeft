<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { APIError, message } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import type { LedgerEntry } from "./chapterTypes";
import type { StoryLedgerEditTarget } from "./storyLedgerTypes";
import {
  initialStoryLedgerView,
  ledgerContexts,
  ledgerKinds,
  ledgerOrigins,
  ledgerPeople,
  storyLedgerPage,
  type StoryLedgerItem,
  type StoryLedgerView,
} from "./storyLedgerReads";

const props = defineProps<{ bookId: string; initialView?: StoryLedgerView }>();
const emit = defineEmits<{
  close: [];
  expired: [];
  edit: [target: StoryLedgerEditTarget];
  view: [value: StoryLedgerView];
}>();
const view = ref<StoryLedgerView>({
  ...initialStoryLedgerView(),
  ...props.initialView,
});
const items = ref<StoryLedgerItem[]>([]),
  cursor = ref(""),
  notes = ref<string[]>([]);
const busy = ref(false),
  error = ref(""),
  notice = ref(""),
  capped = ref(false);
const scanned = ref(0),
  pages = ref(0),
  rowPage = ref(1);
const reads = useBoundedReads({
  stopped: "账本读取已停止。",
  timeout: "账本读取超时，保存稿没有改变，可以重新读取。",
});
const byteLimit = 8 * 1024 * 1024;
let sequence = 0,
  loadedBytes = 0;
const people = computed(() => {
  const names = new Map<string, string>();
  for (const item of items.value) {
    for (const person of item.people)
      names.set(person.id, person.name || person.id);
    for (const entry of item.entries)
      for (const id of ledgerPeople(entry))
        if (!names.has(id))
          names.set(
            id,
            id === entry.subject_id ? entry.subject_name || id : id,
          );
  }
  return [...names]
    .map(([id, name]) => ({ id, name }))
    .sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
});
const personName = (id: string) =>
  people.value.find((person) => person.id === id)?.name ?? id;
const rangeError = computed(() => {
  for (const value of [view.value.minimum, view.value.maximum])
    if (value && (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value))))
      return "故事位置应为非负整数。";
  return view.value.minimum &&
    view.value.maximum &&
    Number(view.value.minimum) > Number(view.value.maximum)
    ? "故事位置起点不能大于终点。"
    : "";
});
const rows = computed(() => {
  if (rangeError.value) return [];
  const needle = view.value.query.trim().toLocaleLowerCase();
  const result = items.value.flatMap((item, chapterOrder) =>
    item.entries.map((entry, entryOrder) => ({
      item,
      entry,
      chapterOrder,
      entryOrder,
    })),
  );
  return result
    .filter(
      ({ item, entry }) =>
        (!view.value.person ||
          ledgerPeople(entry).includes(view.value.person)) &&
        (!view.value.kind || entry.kind === view.value.kind) &&
        (!view.value.context || entry.context === view.value.context) &&
        (!view.value.origin || item.origin === view.value.origin) &&
        (!view.value.freshness ||
          (view.value.freshness === "current"
            ? item.source_matches_current && item.current_ready
            : !item.source_matches_current || !item.current_ready)) &&
        (!view.value.minimum ||
          entry.story_time >= Number(view.value.minimum)) &&
        (!view.value.maximum ||
          entry.story_time <= Number(view.value.maximum)) &&
        (!needle ||
          [
            item.chapter_title,
            item.chapter_id,
            item.summary,
            item.reason,
            entry.statement,
            entry.predicate,
            entry.subject_name,
            ...entry.quotes,
            ...ledgerPeople(entry).flatMap((id) => [id, personName(id)]),
          ].some((text) => text.toLocaleLowerCase().includes(needle))),
    )
    .sort((a, b) => {
      if (view.value.order === "story")
        return (
          a.entry.story_time - b.entry.story_time ||
          a.entry.known_from - b.entry.known_from ||
          a.item.ledger_id.localeCompare(b.item.ledger_id) ||
          a.entryOrder - b.entryOrder
        );
      if (view.value.order === "known")
        return (
          a.entry.known_from - b.entry.known_from ||
          a.entry.story_time - b.entry.story_time ||
          a.item.ledger_id.localeCompare(b.item.ledger_id) ||
          a.entryOrder - b.entryOrder
        );
      return a.chapterOrder - b.chapterOrder || a.entryOrder - b.entryOrder;
    });
});
const pageRows = computed(() =>
  rows.value.slice((rowPage.value - 1) * 50, rowPage.value * 50),
);
const rowPages = computed(() => Math.max(1, Math.ceil(rows.value.length / 50)));
const selectedItem = computed(() =>
  items.value.find((item) => item.ledger_id === view.value.focusedLedger),
);
const selectedEntry = computed(() =>
  selectedItem.value?.entries.find(
    (entry) => entry.id === view.value.focusedEntry,
  ),
);
function select(item: StoryLedgerItem, entry: LedgerEntry) {
  view.value.focusedLedger = item.ledger_id;
  view.value.focusedEntry = entry.id;
  view.value.focusedHash = item.ledger_hash;
}
function target(openLedger: boolean, quote = "") {
  const item = selectedItem.value,
    entry = selectedEntry.value;
  if (
    !item ||
    !entry ||
    !item.control_id ||
    !item.chapter_id ||
    item.origin === "unavailable"
  )
    return;
  emit("edit", {
    chapter_id: item.chapter_id,
    control_id: item.control_id,
    ledger_id: item.ledger_id,
    ledger_hash: item.ledger_hash,
    entry_id: entry.id,
    open_ledger: openLedger,
    quote,
    reviewed_hash: item.source_hash,
    title: `${ledgerKinds[entry.kind]}：${entry.statement.slice(0, 80)}`,
    explanation: `${ledgerOrigins[item.origin]}；${ledgerContexts[entry.context]}；故事位置 ${entry.story_time}，知情位置 ${entry.known_from}。${item.reason || "请根据记录来源核对当前章稿。"}`,
    suggestion:
      "核对当前正文与账本证据；事实、信念、传闻和计划分别处理。需要时修改当前稿并重新核对账本，不把旧引文或匹配结果当作当前事实。",
  });
}
function clearProtected() {
  rowPage.value = 1;
  items.value = [];
  cursor.value = "";
  notes.value = [];
  scanned.value = 0;
  pages.value = 0;
  loadedBytes = 0;
  capped.value = false;
  view.value.focusedLedger = "";
  view.value.focusedEntry = "";
  view.value.focusedHash = "";
  view.value.person = "";
}
function stop() {
  ++sequence;
  busy.value = false;
  notice.value = "已停止继续读取；当前请求返回后不会加入结果或发出下一页。";
}
async function load(
  mode: "refresh" | "more" | "all" = "refresh",
  wantedPages = 1,
) {
  if ((busy.value && mode !== "refresh") || !reads.active()) return;
  const current = ++sequence,
    source = view.value.source,
    book = props.bookId;
  busy.value = true;
  error.value = "";
  notice.value = "";
  let after = mode === "refresh" ? view.value.startCursor : cursor.value;
  let pendingReset = mode === "refresh";
  const maxRequests =
    mode === "all" ? 200 : Math.max(1, Math.min(200, wantedPages));
  const seen = new Set<string>();
  try {
    for (let request = 0; request < maxRequests; request++) {
      if (current !== sequence || !reads.active()) return;
      const raw = await reads.read<unknown>(
        `/novels/${encodeURIComponent(book)}/story-ledger?source=${source}${after ? `&after=${encodeURIComponent(after)}` : ""}`,
      );
      if (current !== sequence || !reads.active()) return;
      const page = storyLedgerPage(raw, book, source);
      if (
        page.next_cursor &&
        (page.next_cursor === after || seen.has(page.next_cursor))
      )
        throw new Error("账本分页没有向前推进，已停止继续读取，请刷新。");
      seen.add(after);
      const bytes = new TextEncoder().encode(JSON.stringify(page)).length;
      if (
        !pendingReset &&
        (items.value.length + page.items.length > 1000 ||
          loadedBytes + bytes > byteLimit)
      ) {
        capped.value = true;
        notice.value = "本批已到浏览容量上限，可清空已读结果后继续下一批。";
        break;
      }
      if (pendingReset) {
        rowPage.value = 1;
        items.value = [];
        scanned.value = 0;
        pages.value = 0;
        loadedBytes = 0;
        capped.value = false;
        pendingReset = false;
      }
      items.value = [
        ...new Map(
          [...items.value, ...page.items].map((item) => [item.ledger_id, item]),
        ).values(),
      ];
      scanned.value += page.scanned;
      loadedBytes += bytes;
      pages.value++;
      cursor.value = page.next_cursor;
      after = page.next_cursor;
      notes.value = page.notes;
      view.value.pages = pages.value;
      if (
        selectedItem.value &&
        view.value.focusedHash &&
        selectedItem.value.ledger_hash !== view.value.focusedHash
      ) {
        notice.value = "所选账本已换版；已按同一条目ID定位，请重新核对来源。";
      }
      if (!after) break;
      if (pages.value >= 200 || items.value.length >= 1000) {
        capped.value = true;
        break;
      }
    }
  } catch (e) {
    if (current !== sequence || !reads.active()) return;
    error.value = message(e);
    if (e instanceof APIError && [401, 403].includes(e.status)) {
      clearProtected();
      if (e.status === 401) emit("expired");
    } else if (items.value.length)
      notice.value = "保留上次已读取结果；这些状态尚未重新核验。";
  } finally {
    if (current === sequence && reads.active()) busy.value = false;
  }
}
function nextWindow() {
  if (!cursor.value || busy.value) return;
  view.value.startCursor = cursor.value;
  view.value.focusedLedger = "";
  view.value.focusedEntry = "";
  view.value.focusedHash = "";
  void load("refresh");
}
watch(view, (value) => emit("view", { ...value }), { deep: true });
watch(rowPages, (count) => {
  rowPage.value = Math.max(1, Math.min(rowPage.value, count));
});
watch(
  () => [
    view.value.query,
    view.value.person,
    view.value.kind,
    view.value.context,
    view.value.origin,
    view.value.freshness,
    view.value.minimum,
    view.value.maximum,
    view.value.order,
  ],
  () => (rowPage.value = 1),
);
watch(
  () => view.value.source,
  () => {
    ++sequence;
    busy.value = false;
    clearProtected();
    view.value.startCursor = "";
    view.value.pages = 1;
    void load();
  },
  { flush: "sync" },
);
watch(
  () => props.bookId,
  () => {
    ++sequence;
    busy.value = false;
    clearProtected();
    view.value = initialStoryLedgerView();
    void load();
  },
  { flush: "sync" },
);
onMounted(() => load("refresh", view.value.pages));
</script>

<template>
  <section class="story-ledger" aria-label="全书剧情账本">
    <header class="actions">
      <h1>全书剧情账本</h1>
      <button @click="emit('close')">返回小说工作台</button>
    </header>
    <p>
      集中查看已保存的事件、人物状态、伏笔和高光记录。私人账本尚未成为正史；信念、传闻、梦境与未发生计划分别标注。这里不自动判断伏笔回收或合成人物当前状态。
    </p>
    <div class="filters panel">
      <label
        >账本来源<select v-model="view.source">
          <option value="working">当前工作账本（本人保存稿优先）</option>
          <option value="published">只看正式采用账本</option>
        </select></label
      >
      <label
        >搜索已加载账本<input
          v-model="view.query"
          type="search"
          placeholder="人物名、关键词、章节或原文引文"
      /></label>
      <label
        >关联人物<select v-model="view.person">
          <option value="">全部人物与身份</option>
          <option v-for="person in people" :key="person.id" :value="person.id">
            {{ person.name }} · {{ person.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <label
        >条目类型<select v-model="view.kind">
          <option value="">全部类型</option>
          <option v-for="(name, id) in ledgerKinds" :key="id" :value="id">
            {{ name }}
          </option>
        </select></label
      >
      <label
        >排序<select v-model="view.order">
          <option value="chapter">账本载入顺序</option>
          <option value="story">故事发生位置</option>
          <option value="known">知情位置</option>
        </select></label
      >
    </div>
    <details class="panel">
      <summary>更多筛选</summary>
      <div class="filters">
        <label
          >记录语境<select v-model="view.context">
            <option value="">全部语境</option>
            <option v-for="(name, id) in ledgerContexts" :key="id" :value="id">
              {{ name }}
            </option>
          </select></label
        >
        <label
          >记录来源<select v-model="view.origin">
            <option value="">全部来源</option>
            <option value="private">本人保存的私人账本</option>
            <option value="published">正式采用账本</option>
          </select></label
        >
        <label
          >当前核对状态<select v-model="view.freshness">
            <option value="">全部状态</option>
            <option value="current">源稿一致且当前依据有效</option>
            <option value="review">需要核对</option>
          </select></label
        >
        <label
          >故事位置从<input v-model="view.minimum" inputmode="numeric" /></label
        ><label
          >故事位置到<input v-model="view.maximum" inputmode="numeric"
        /></label>
      </div>
    </details>
    <div class="actions">
      <button :disabled="busy" @click="load('refresh', pages || view.pages)">
        刷新账本
      </button>
      <button v-if="cursor && !capped" :disabled="busy" @click="load('more')">
        继续读取账本
      </button>
      <button v-if="cursor && !capped" :disabled="busy" @click="load('all')">
        读取其余账本
      </button>
      <button v-if="busy" @click="stop">停止继续读取</button>
      <button v-if="capped && cursor" :disabled="busy" @click="nextWindow">
        清空已读结果并继续下一批
      </button>
    </div>
    <p role="status">
      {{ busy ? "正在读取… " : "" }}本批已扫描 {{ scanned }} 个账本资源，已加载
      {{ items.length }} 本账本、{{
        items.reduce((n, item) => n + item.entries.length, 0)
      }}
      条记录；当前筛选 {{ rows.length }} 条。{{
        cursor ? "仍有账本未读取。" : "本批已到已存在账本资源列表末尾。"
      }}
    </p>
    <p class="muted">
      筛选和排序只覆盖本批已加载记录。没有创建账本的章节不在列表，未保存编辑也不计入。故事与知情位置是作者填写的整数，不代表日期或叙事次序。
    </p>
    <p v-if="view.startCursor" class="muted">
      当前从上一批之后继续读取，不包含之前批次的条目。
    </p>
    <p v-for="note in notes" :key="note" class="muted">{{ note }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="rangeError" role="alert">{{ rangeError }}</p>
    <ul
      v-if="items.some((item) => !item.entries.length)"
      aria-label="未展示条目的账本"
    >
      <li
        v-for="item in items.filter((item) => !item.entries.length)"
        :key="item.ledger_id"
      >
        {{ item.chapter_title || item.chapter_id || item.ledger_id }}：{{
          item.reason || "本账本尚无已保存条目。"
        }}
      </li>
    </ul>
    <p v-if="!rows.length && !busy">
      当前筛选下没有已加载条目；请检查筛选、未读取页或各章账本状态。
    </p>
    <ol class="entry-list" aria-label="剧情账本条目">
      <li
        v-for="row in pageRows"
        :key="`${row.item.ledger_id}:${row.item.ledger_hash}:${row.entry.id}`"
      >
        <span
          >{{ row.item.chapter_title || row.item.chapter_id }} ·
          {{ ledgerKinds[row.entry.kind] }} ·
          {{ ledgerContexts[row.entry.context] }} · 故事
          {{ row.entry.story_time }} / 知情 {{ row.entry.known_from }}</span
        >
        <p>{{ row.entry.statement }}</p>
        <p class="muted">
          {{ ledgerOrigins[row.item.origin]
          }}{{
            !row.item.source_matches_current || !row.item.current_ready
              ? " · 需要核对"
              : ""
          }}
        </p>
        <button
          :aria-label="`查看条目 ${row.item.ledger_id} ${row.entry.id}`"
          @click="select(row.item, row.entry)"
        >
          查看记录与证据
        </button>
      </li>
    </ol>
    <nav v-if="rowPages > 1" aria-label="已加载条目分页">
      <button :disabled="rowPage <= 1" @click="rowPage--">上一页</button
      ><span>第 {{ rowPage }} / {{ rowPages }} 页</span
      ><button :disabled="rowPage >= rowPages" @click="rowPage++">
        下一页
      </button>
    </nav>
    <section
      v-if="selectedItem && selectedEntry"
      class="panel"
      aria-label="所选账本条目"
    >
      <h2>
        {{ ledgerKinds[selectedEntry.kind] }} · {{ selectedItem.chapter_title }}
      </h2>
      <p>{{ selectedEntry.statement }}</p>
      <p>
        {{ ledgerOrigins[selectedItem.origin] }} ·
        {{ ledgerContexts[selectedEntry.context] }} · 故事位置
        {{ selectedEntry.story_time }} / 知情位置 {{ selectedEntry.known_from }}
      </p>
      <p>{{ selectedItem.summary }}</p>
      <p v-if="selectedItem.reason" role="status">{{ selectedItem.reason }}</p>
      <p v-if="selectedItem.source_matches_current">
        原账本引用的章稿与当前已保存章稿一致。
      </p>
      <p v-else>
        原账本来源与当前章稿不一致或无法核实，旧记录供回查，不能当作当前事实。
      </p>
      <p>
        {{
          selectedItem.current_ready
            ? "当前章稿写作依据有效。"
            : "当前章稿写作依据需要重新核对。"
        }}
      </p>
      <dl>
        <template v-if="selectedEntry.subject_id || selectedEntry.subject_name"
          ><dt>主体</dt>
          <dd>
            {{
              personName(selectedEntry.subject_id) || selectedEntry.subject_name
            }}
            · {{ selectedEntry.subject_id }}（记录名：{{
              selectedEntry.subject_name
            }}）
          </dd></template
        ><template v-if="selectedEntry.object_id"
          ><dt>关联对象</dt>
          <dd>
            {{ personName(selectedEntry.object_id) }} ·
            {{ selectedEntry.object_id }}
          </dd></template
        ><template v-if="selectedEntry.holder_id"
          ><dt>信念持有人</dt>
          <dd>
            {{ personName(selectedEntry.holder_id) }} ·
            {{ selectedEntry.holder_id }}
          </dd></template
        >
        <dt>可知范围</dt>
        <dd>
          {{ selectedEntry.visibility
          }}<span v-for="id in selectedEntry.known_by" :key="id">
            · {{ personName(id) }}（{{ id }}）</span
          >
        </dd>
      </dl>
      <h3>原账本引文</h3>
      <div v-for="(quote, index) in selectedEntry.quotes" :key="index">
        <blockquote>{{ quote }}</blockquote>
        <button
          :disabled="
            !selectedItem.control_id || selectedItem.origin === 'unavailable'
          "
          :aria-label="`定位引文并打开章稿 ${index + 1}`"
          @click="target(false, quote)"
        >
          定位引文并打开章稿
        </button>
      </div>
      <p>
        定位会重新核对当前文字。旧偏移不直接套到新正文，引文消失不表示伏笔已回收或问题已解决。
      </p>
      <button
        :disabled="
          !selectedItem.control_id || selectedItem.origin === 'unavailable'
        "
        @click="target(true, selectedEntry.quotes[0] ?? '')"
      >
        打开对应账本
      </button>
      <details>
        <summary>版本与身份详情</summary>
        <p>
          章ID {{ selectedItem.chapter_id }}；账本ID
          {{ selectedItem.ledger_id }}；条目ID {{ selectedEntry.id }}
        </p>
        <p>
          账本版本 {{ selectedItem.ledger_hash }}；原章稿
          {{ selectedItem.source_hash }}；当前章稿
          {{ selectedItem.current_hash }}
        </p>
      </details>
    </section>
    <p v-else-if="view.focusedLedger && !busy" role="status">
      原选中条目未在当前已读结果中找到，可能已改版、移出或不可读，请重新选择。
    </p>
  </section>
</template>
<style scoped>
.story-ledger {
  display: grid;
  gap: 14px;
}
.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: end;
}
.filters label {
  display: grid;
  gap: 6px;
  flex: 1 1 180px;
  min-width: 0;
}
.filters input,
.filters select {
  max-width: 100%;
}
.entry-list {
  display: grid;
  gap: 12px;
  padding-left: 24px;
}
.entry-list li {
  border-bottom: 1px solid var(--border, #ddd);
  padding: 10px 0;
  overflow-wrap: anywhere;
}
blockquote {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  border-left: 3px solid #c7af83;
  margin: 14px 0;
  padding: 8px 12px;
}
dl,
details p {
  overflow-wrap: anywhere;
}
nav {
  display: flex;
  gap: 12px;
  align-items: center;
}
</style>
