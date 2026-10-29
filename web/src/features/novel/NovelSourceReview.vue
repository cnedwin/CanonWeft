<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { APIError, message } from "../../api";
import { useBoundedReads } from "../../composables/useBoundedReads";
const props = defineProps<{
  bookId: string;
  title: string;
  initialFilters?: { query: string; kind: string };
}>();
interface Item {
  resource_id: string;
  name: string;
  kind: string;
  status: string;
  chapter_id?: string;
  reasons: string[];
}
const emit = defineEmits<{
  close: [];
  expired: [];
  research: [id: string];
  chapter: [id: string];
  planning: [resourceId: string];
  materials: [];
  filters: [value: { query: string; kind: string }];
}>();
const items = ref<Item[]>([]),
  busy = ref(false),
  error = ref(""),
  cursor = ref(""),
  scanned = ref(0),
  query = ref(props.initialFilters?.query ?? ""),
  kind = ref(props.initialFilters?.kind ?? "all");
watch([query, kind], () =>
  emit("filters", { query: query.value, kind: kind.value }),
);
const { read, active } = useBoundedReads({
  stopped: "待复核清单读取已停止。",
  timeout: "待复核清单读取超时，原稿未改动，可重新检查。",
});
const kindNames: Record<string, string> = {
  chapter: "章节正文",
  planning: "故事与章节规划",
  research: "研究稿",
  material: "参考资料",
};
const visible = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase();
  return items.value.filter(
    (item) =>
      (kind.value === "all" ||
        (kind.value === "withdrawn"
          ? item.status === "withdrawn"
          : item.kind === kind.value)) &&
      (!needle ||
        [
          item.name,
          item.resource_id,
          item.chapter_id ?? "",
          ...item.reasons,
        ].some((text) => text.toLocaleLowerCase().includes(needle))),
  );
});
const groups = computed(() =>
  Object.entries(kindNames)
    .map(([id, title]) => ({
      id,
      title,
      items: visible.value.filter(
        (item) => (kindNames[item.kind] ? item.kind : "material") === id,
      ),
    }))
    .filter((group) => group.items.length),
);
async function load(more = false) {
  if (busy.value || !active()) return;
  busy.value = true;
  error.value = "";
  try {
    const p = await read<{
      items: Item[];
      next_cursor?: string;
      scanned: number;
    }>(
      `/novels/${props.bookId}/source-review${more ? `?after=${encodeURIComponent(cursor.value)}` : ""}`,
    );
    if (!active()) return;
    if (
      !Array.isArray(p.items) ||
      !Number.isSafeInteger(p.scanned) ||
      p.scanned < 0 ||
      p.items.some(
        (item) =>
          !item ||
          ![item.resource_id, item.name, item.kind, item.status].every(
            (value) => typeof value === "string",
          ) ||
          !Array.isArray(item.reasons) ||
          item.reasons.some((reason) => typeof reason !== "string") ||
          (item.chapter_id !== undefined &&
            typeof item.chapter_id !== "string"),
      ) ||
      (p.next_cursor !== undefined && typeof p.next_cursor !== "string")
    )
      throw new Error("待复核清单未完整返回，请重新检查。");
    items.value = [
      ...new Map(
        (more ? [...items.value, ...p.items] : p.items).map((item) => [
          item.resource_id,
          item,
        ]),
      ).values(),
    ];
    scanned.value = (more ? scanned.value : 0) + p.scanned;
    cursor.value = p.next_cursor ?? "";
  } catch (e) {
    if (active()) {
      error.value = message(e);
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        items.value = [];
        cursor.value = "";
        scanned.value = 0;
      }
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (active()) busy.value = false;
  }
}
onMounted(() => load());
</script>
<template>
  <section class="source-review" aria-label="资料变更与待复核">
    <header>
      <div>
        <p>{{ title }}</p>
        <h1>资料变更与待复核</h1>
      </div>
      <button @click="emit('close')">返回小说工作台</button>
    </header>
    <p>
      集中查看撤回来源，以及需要核对的私人研究稿、规划和章节。这里只读检查；进入原编辑器处理，不自动改写或确认作品。正式资料的跨模块影响与修复仍在原变更清单中。
    </p>
    <button :disabled="busy" @click="load()">重新检查资料状态</button>
    <button @click="emit('materials')">查看资料与变更影响清单</button>
    <p v-if="error" role="alert">
      {{ error
      }}<span v-if="items.length"> 下方保留上次清单，请重新检查后核对。</span>
    </p>
    <p v-if="busy" role="status">正在检查资料与作者稿…</p>
    <p v-else>
      本次已检查 {{ scanned }} 份资料与章稿，显示 {{ items.length }} 项{{
        cursor ? "；还有资料尚未检查" : "。"
      }}。检查结果只对应读取时的状态。
    </p>
    <p v-if="!busy && !error && !items.length">
      当前已检查范围未发现来源变更或待复核稿件。
    </p>
    <div class="review-filters">
      <label
        >查找待复核项<input
          v-model="query"
          type="search"
          maxlength="200"
          placeholder="标题、编号或变更原因"
      /></label>
      <label
        >待复核类型<select v-model="kind">
          <option value="all">全部类型</option>
          <option value="chapter">章节正文</option>
          <option value="planning">故事与章节规划</option>
          <option value="research">研究稿</option>
          <option value="material">参考资料</option>
          <option value="withdrawn">已撤回来源</option>
        </select></label
      >
    </div>
    <p>
      筛选显示 {{ visible.length }} 项，搜索仅覆盖已检查的资料；{{
        cursor ? "可继续检查后续资料。" : "不代表未记录的语义影响均已排除。"
      }}
    </p>
    <p v-if="items.length && !visible.length">
      没有匹配项，可调整关键词或类型。
    </p>
    <section
      v-for="group in groups"
      :key="group.id"
      :aria-label="group.title + '待复核项'"
    >
      <h2>{{ group.title }} · {{ group.items.length }}</h2>
      <article
        v-for="item in group.items"
        :key="item.resource_id"
        :aria-label="item.name"
      >
        <h3>{{ item.name }}</h3>
        <strong>{{
          item.status === "withdrawn" ? "已撤回使用" : "需要复核"
        }}</strong>
        <ul>
          <li v-for="(reason, i) in item.reasons" :key="i">{{ reason }}</li>
        </ul>
        <button
          v-if="item.kind === 'research'"
          @click="emit('research', item.resource_id)"
        >
          打开这份研究稿
        </button>
        <button
          v-else-if="item.kind === 'chapter' && item.chapter_id"
          @click="emit('chapter', item.chapter_id)"
        >
          打开这章正文
        </button>
        <button
          v-else-if="item.kind === 'planning'"
          @click="emit('planning', item.resource_id)"
        >
          打开这份规划核对
        </button>
        <button v-else @click="emit('materials')">前往资料与变更清单</button>
      </article>
    </section>
    <button v-if="cursor" :disabled="busy" @click="load(true)">
      继续检查更多资料
    </button>
  </section>
</template>
<style scoped>
.source-review {
  max-width: 1100px;
  margin: auto;
}
header {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  align-items: center;
}
article {
  padding: 1rem;
  border: 1px solid #ced6d5;
  border-radius: 0.6rem;
  margin-block: 1rem;
}
button {
  margin: 0.3rem 0.5rem 0.3rem 0;
}
li {
  overflow-wrap: anywhere;
}
.review-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
  margin: 1rem 0;
}
.review-filters label {
  display: grid;
  gap: 0.4rem;
  flex: 1 1 240px;
}
</style>
