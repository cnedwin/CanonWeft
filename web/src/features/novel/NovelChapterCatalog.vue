<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { APIError, message } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
import type { NovelBook } from "./types";
import { useManuscriptReads } from "./useManuscriptReads";
import {
  filterCatalog,
  manuscriptCatalog,
  type CatalogChapter,
  type CatalogFilter,
  type ChapterReadiness,
} from "./manuscriptCatalog";

const props = defineProps<{ book: NovelBook; enabled: boolean }>();
const emit = defineEmits<{
  chapter: [id: string];
  planning: [];
  expired: [];
}>();
const snapshots = useManuscriptReads(props.book.id);
const { read, active } = useBoundedReads({
  stopped: "章节目录读取已停止。",
  timeout: "章节目录读取超时，原稿未改动，请重新读取。",
});
const rows = ref<CatalogChapter[]>([]),
  busy = ref(false),
  error = ref(""),
  checkedAt = ref("");
const query = ref(""),
  filter = ref<CatalogFilter>("all"),
  volume = ref("");
let request = 0;
const visible = computed(() =>
  filterCatalog(rows.value, query.value, filter.value, volume.value),
);
const volumes = computed(() => [
  ...new Map(
    rows.value.map((row) => [row.current.volume_id, row.current.volume]),
  ).entries(),
]);
const groups = computed(() =>
  volumes.value
    .map(([id, title]) => ({
      id,
      title,
      rows: visible.value.filter((row) => row.current.volume_id === id),
    }))
    .filter((group) => group.rows.length),
);
const written = computed(
  () => rows.value.filter((row) => row.current.included).length,
);
const adopted = computed(
  () => rows.value.filter((row) => row.adopted.included).length,
);
const characters = computed(() =>
  rows.value.reduce(
    (sum, row) => sum + (row.current.included ? row.current.characters : 0),
    0,
  ),
);
const nextChapter = computed(() =>
  rows.value.find((row) => !row.current.included && row.ready),
);
const warnings = computed(() => rows.value.filter((row) => row.review).length);
function reset() {
  rows.value = [];
  checkedAt.value = "";
}
async function refresh() {
  if (!props.enabled || !active()) return;
  const sequence = ++request;
  snapshots.cancel();
  busy.value = true;
  error.value = "";
  try {
    const language = props.book.setup.language.includes("繁")
      ? "zh-Hant"
      : "zh-Hans";
    const results = await Promise.allSettled([
      snapshots.preview({ source: "draft", language }),
      snapshots.preview({ source: "published", language }),
      read<{ items: ChapterReadiness[] }>(
        `/novels/${props.book.id}/writing-batches/chapters`,
      ),
    ] as const);
    if (!active() || sequence !== request) return;
    const denial = results.find(
      (result) =>
        result.status === "rejected" &&
        result.reason instanceof APIError &&
        [401, 403].includes(result.reason.status),
    );
    if (denial?.status === "rejected") throw denial.reason;
    const [current, published, details] = results;
    if (current.status === "rejected") throw current.reason;
    if (published.status === "rejected") throw published.reason;
    if (details.status === "rejected") throw details.reason;
    rows.value = manuscriptCatalog(
      current.value,
      published.value,
      details.value.items,
    );
    checkedAt.value = new Date().toLocaleTimeString();
  } catch (e) {
    if (!active() || sequence !== request) return;
    if (e instanceof APIError && [401, 403].includes(e.status)) {
      reset();
      if (e.status === 401) emit("expired");
    }
    error.value = message(e);
  } finally {
    if (active() && sequence === request) busy.value = false;
  }
}
function open(row: CatalogChapter) {
  if (!busy.value && row.openable) emit("chapter", row.current.id);
}
watch(
  () => props.enabled,
  (enabled) => {
    if (enabled) void refresh();
    else {
      ++request;
      snapshots.cancel();
      reset();
      busy.value = false;
      error.value = "";
    }
  },
  { immediate: true },
);
</script>

<template>
  <section class="panel manuscript-catalog" aria-label="本书章节目录">
    <header class="catalog-heading">
      <div>
        <h2>本书章节</h2>
        <p class="muted">分卷查看稿件、找到待写章节。打开章节不会调用模型。</p>
      </div>
      <button type="button" :disabled="busy || !enabled" @click="refresh">
        刷新章节目录
      </button>
    </header>
    <template v-if="!enabled"
      ><p>确认全书和分卷规划后，这里会显示章节与稿件进度。</p>
      <button type="button" @click="emit('planning')">
        前往章节规划
      </button></template
    >
    <template v-else>
      <p v-if="busy" role="status">正在核对本书章节与稿件…</p>
      <p v-if="error" role="alert">
        {{ error
        }}<span v-if="rows.length">
          下方保留上次读取结果，请刷新后再核对。</span
        >
      </p>
      <p v-if="checkedAt" class="muted">
        目录快照
        {{ checkedAt }}；仅统计已保存稿，正在编辑而未保存的内容不在其中。
      </p>
      <p aria-label="全书稿件统计">
        全书 {{ rows.length }} 章 · 有正文 {{ written }} 章 · 当前可用稿
        {{ characters.toLocaleString() }} 个字符 · 已采用 {{ adopted }} 章 ·
        待核对 {{ warnings }} 章
      </p>
      <progress
        :value="written"
        :max="Math.max(1, rows.length)"
        aria-label="已有正文的章节比例"
      ></progress>
      <p class="muted">
        当前可用稿优先使用你的私人稿，没有私人稿时回退已采用稿。字符数含空白，不代表质量或正式发布进度。
      </p>
      <div class="catalog-filters">
        <label
          >查找章节<input
            v-model="query"
            type="search"
            maxlength="200"
            placeholder="章节标题、分卷或章节编号"
        /></label>
        <label
          >稿件状态<select v-model="filter" aria-label="稿件状态">
            <option value="all">全部章节</option>
            <option value="written">已有正文</option>
            <option value="draft">有私人稿</option>
            <option value="adopted">有已采用稿</option>
            <option value="empty">待写正文</option>
            <option value="review">需要核对依据</option>
          </select></label
        >
        <label
          >分卷范围<select v-model="volume" aria-label="分卷范围">
            <option value="">全书</option>
            <option v-for="[id, title] in volumes" :key="id" :value="id">
              {{ title }}
            </option>
          </select></label
        >
      </div>
      <div class="catalog-heading">
        <p>当前显示 {{ visible.length }} 章</p>
        <button
          type="button"
          :disabled="busy || !nextChapter"
          @click="nextChapter && open(nextChapter)"
        >
          打开下一待写章</button
        ><button type="button" @click="emit('planning')">查看或修改规划</button>
      </div>
      <p v-if="!rows.length && !busy">
        暂无可显示的章节；请先核对全书及分卷规划。
      </p>
      <p v-else-if="!visible.length">
        没有匹配章节，可调整关键词、状态或分卷范围。
      </p>
      <section
        v-for="group in groups"
        :key="group.id"
        class="catalog-volume"
        :aria-label="`${group.title}章节`"
      >
        <h3>{{ group.title }}</h3>
        <ol class="catalog-chapters">
          <li
            v-for="row in group.rows"
            :key="row.current.id"
            :value="row.position"
          >
            <div>
              <strong>{{ row.current.title }}</strong>
              <p>
                {{
                  row.current.included
                    ? `${row.current.characters.toLocaleString()} 个字符`
                    : "尚无正文"
                }}
                ·
                {{
                  row.privateDraft
                    ? row.current.included
                      ? "私人稿已保存"
                      : "已保存空稿"
                    : row.current.included
                      ? "使用已采用稿"
                      : "尚未保存稿件"
                }}{{ row.adopted.included ? " · 有已采用稿" : "" }}
              </p>
              <p v-if="row.different" class="muted">
                私人稿与已采用版本不同，尚未自动采用。
              </p>
              <p v-if="row.current.warning" class="muted">
                {{ row.current.warning }}
              </p>
              <p v-if="!row.ready" class="muted">
                {{ row.reason || "本章细纲状态待核对"
                }}{{
                  row.current.resource_id ? "；已有稿件仍可打开编辑。" : ""
                }}
              </p>
            </div>
            <button
              type="button"
              :disabled="busy || !row.openable"
              :aria-label="`打开第${row.position}章 · ${row.current.title}`"
              @click="open(row)"
            >
              打开章节
            </button>
          </li>
        </ol>
      </section>
    </template>
  </section>
</template>

<style scoped>
.manuscript-catalog {
  display: grid;
  gap: 0.8rem;
}
.catalog-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 0.75rem;
}
.catalog-heading h2,
.catalog-heading p,
.catalog-chapters p {
  margin: 0.35rem 0;
}
.catalog-filters {
  display: grid;
  grid-template-columns: 2fr 1fr 1fr;
  gap: 0.75rem;
}
.catalog-filters label {
  display: grid;
  gap: 0.4rem;
}
progress {
  width: 100%;
  accent-color: #53746a;
}
.catalog-chapters {
  padding-left: 1.8rem;
}
.catalog-chapters li {
  display: list-item;
  padding: 0.8rem 0.4rem;
  border-bottom: 1px solid #dedbd4;
}
.catalog-chapters li > div {
  display: inline-block;
  width: calc(100% - 9rem);
  vertical-align: middle;
}
.catalog-chapters li > button {
  margin-left: 0.75rem;
}
@media (max-width: 760px) {
  .catalog-filters {
    grid-template-columns: 1fr;
  }
  .catalog-chapters li > div {
    display: block;
    width: auto;
  }
  .catalog-chapters li > button {
    margin: 0.5rem 0;
  }
}
</style>
