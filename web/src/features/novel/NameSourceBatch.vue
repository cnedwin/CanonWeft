<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import { api, APIError, message } from "../../api";
const props = defineProps<{ root: string; blocked: boolean }>();
const emit = defineEmits<{ preview: [string[]]; expired: [] }>();
interface Source {
  id: string;
  source: {
    title: string;
    language: string;
    captured_at: string;
    license: string;
  };
}
const items = ref<Source[]>([]),
  selected = ref<string[]>([]),
  next = ref(""),
  busy = ref(false),
  error = ref(""),
  loaded = ref(false);
let disposed = false;
onBeforeUnmount(() => {
  disposed = true;
});
async function load(after = "") {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  try {
    const result = await api<{ items: Source[]; next: string }>(
      `${props.root}/name-catalog/sources?after=${encodeURIComponent(after)}`,
    );
    if (!disposed) {
      items.value = result.items;
      next.value = result.next;
      loaded.value = true;
    }
  } catch (e) {
    if (!disposed) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (!disposed) busy.value = false;
  }
}
function selectPage() {
  const ids = [
    ...new Set([...selected.value, ...items.value.map((item) => item.id)]),
  ];
  if (ids.length > 100) {
    error.value = "每批最多100份原件，请先预览当前选择。";
    return;
  }
  selected.value = ids;
}
</script>
<template>
  <details aria-label="批量整理名字原件">
    <summary>批量整理已采集的名字</summary>
    <p>
      研究任务后台采集的姓名原件可以在这里一次整理。只读当前作品分支中你的原件；最多100份，预览不保存、不覆盖已有编辑。
    </p>
    <button :disabled="busy || blocked" @click="load()">读取已采集名字</button>
    <button :disabled="busy || blocked || !items.length" @click="selectPage">
      选择本页名字原件
    </button>
    <button
      :disabled="busy || blocked || !selected.length"
      @click="selected = []"
    >
      清空原件选择
    </button>
    <p v-if="busy" role="status">正在读取名字原件…</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="loaded">
      本页{{ items.length }}份 · 已选择{{ selected.length }}份；{{
        next ? "还有后续原件，可继续翻页" : "已到当前原件列表末页"
      }}。
    </p>
    <ul>
      <li v-for="item in items" :key="item.id">
        <label
          ><input
            v-model="selected"
            type="checkbox"
            :value="item.id"
            :disabled="
              busy ||
              blocked ||
              (!selected.includes(item.id) && selected.length >= 100)
            "
          />选择名字原件：{{ item.source.title }} · {{ item.source.language }} ·
          {{ item.source.license }} · {{ item.source.captured_at }}</label
        >
      </li>
    </ul>
    <button v-if="next" :disabled="busy || blocked" @click="load(next)">
      下一页名字原件
    </button>
    <button
      :disabled="busy || blocked || !selected.length"
      @click="emit('preview', [...selected])"
    >
      预览所选名字入库
    </button>
  </details>
</template>
