<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { api, APIError, message } from "../../api";
const props = defineProps<{ bookId: string; title: string }>();
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
  planning: [];
  materials: [];
}>();
const items = ref<Item[]>([]),
  busy = ref(false),
  error = ref(""),
  cursor = ref(""),
  scanned = ref(0);
let disposed = false;
onBeforeUnmount(() => {
  disposed = true;
});
async function load(more = false) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    const p = await api<{
      items: Item[];
      next_cursor?: string;
      scanned: number;
    }>(
      `/novels/${props.bookId}/source-review${more ? `?after=${encodeURIComponent(cursor.value)}` : ""}`,
    );
    if (disposed) return;
    items.value = more ? [...items.value, ...p.items] : p.items;
    scanned.value = (more ? scanned.value : 0) + p.scanned;
    cursor.value = p.next_cursor ?? "";
  } catch (e) {
    if (!disposed) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (!disposed) busy.value = false;
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
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="busy" role="status">正在检查资料与作者稿…</p>
    <p v-else>
      本次已检查 {{ scanned }} 份资料与章稿，显示 {{ items.length }} 项{{
        cursor ? "；还有资料尚未检查" : "。"
      }}。检查结果只对应读取时的状态。
    </p>
    <p v-if="!busy && !error && !items.length">
      当前已检查范围未发现来源变更或待复核稿件。
    </p>
    <article
      v-for="item in items"
      :key="item.resource_id"
      :aria-label="item.name"
    >
      <h2>{{ item.name }}</h2>
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
      <button v-else-if="item.kind === 'planning'" @click="emit('planning')">
        前往规划台核对
      </button>
      <button v-else @click="emit('materials')">前往资料与变更清单</button>
    </article>
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
</style>
