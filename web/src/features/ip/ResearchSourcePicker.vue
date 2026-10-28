<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import { researchSources, researchSourceLabel } from "./researchSources";
const props = defineProps<{
  projectId: string;
  branchId: string;
  blocked: boolean;
  purpose?: "names";
}>();
const emit = defineEmits<{
  selected: [string];
  blocking: [boolean];
  expired: [];
}>();
interface Hit {
  page_id: number;
  title: string;
  snippet: string;
  url: string;
}
interface Saved {
  id: string;
  source: {
    provider: string;
    source_version?: string;
    title: string;
    url: string;
    captured_at: string;
    revision_id: number;
    query: string;
  };
}
const root = `/projects/${props.projectId}/branches/${props.branchId}/research-sources`;
const language = ref("zh");
const languageNames: Record<string, string> = {
  zh: "中文",
  en: "English",
  ja: "日本語",
  fr: "Français",
  de: "Deutsch",
};
const source = ref(props.purpose === "names" ? "wikidata_names" : "wikipedia");
const sources = researchSources.filter(
  (item) => !props.purpose || item.id === "wikidata_names",
);
const languages = computed(
  () =>
    researchSources.find((item) => item.id === source.value)?.languages ?? [],
);
const visibleHistory = computed(() =>
  history.value.filter(
    (item) => !props.purpose || item.source.provider === "wikidata_names",
  ),
);
function changeSource() {
  if (!languages.value.includes(language.value))
    language.value = languages.value[0] ?? "en";
  hits.value = [];
}
const query = ref("");
const searched = ref({ provider: "", language: "", query: "" });
const hits = ref<Hit[]>([]);
const history = ref<Saved[]>([]);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const operations = new Map<string, string>();
let disposed = false;
watch(busy, (value) => emit("blocking", value), { flush: "sync" });
function fail(e: unknown) {
  if (disposed) return;
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function refresh() {
  const result = await api<{ items: Saved[] }>(root);
  if (!disposed) history.value = result.items;
}
async function search() {
  if (busy.value || props.blocked || !query.value.trim()) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  const input = {
    provider: source.value === "wikipedia" ? "" : source.value,
    language: language.value,
    query: query.value.trim(),
  };
  try {
    const result = await api<{ items: Hit[] }>(`${root}/search`, "POST", input);
    if (disposed) return;
    searched.value = input;
    hits.value = result.items;
    if (!result.items.length)
      notice.value = "未找到结果，可改用专业术语、年代或地域关键词。";
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function collect(hit: Hit) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  const key = JSON.stringify([
    searched.value.provider,
    searched.value.language,
    searched.value.query,
    hit.page_id,
  ]);
  const operation = operations.get(key) ?? newOperationID();
  operations.set(key, operation);
  try {
    const result = await api<{ id: string }>(`${root}/collect`, "POST", {
      ...searched.value,
      page_id: hit.page_id,
      operation_id: operation,
      ...(searched.value.provider === "official_reference"
        ? { reference_url: hit.url }
        : {}),
    });
    if (disposed) return;
    notice.value =
      searched.value.provider === "official_reference"
        ? "已保存来源、网页指纹与有限摘录，下载文件为采集凭据，未保存整页HTML。请核对原网页后明确接受部分材料。"
        : "已保存原始接口数据与来源版本。下方可编辑文字，确认后再作为创作资料采用。";
    // The receipt is usable even when the history refresh fails.
    emit("selected", result.id);
    await refresh();
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
onMounted(() => {
  void refresh().catch(fail);
});
onBeforeUnmount(() => {
  disposed = true;
  emit("blocking", false);
});
</script>
<template>
  <details class="research-source-picker">
    <summary>按题材搜索并采集公开资料</summary>
    <p>
      输入题材关键词或名字线索。百科用于定位术语；开放论文适合医学与生命科学；姓名条目只提供素材。搜索词会发送给所选公开服务，请勿填写私人原稿。
    </p>
    <p class="muted">
      每次最多5条，逐条采集；不抓取图片、不运行网页代码。原始数据、固定版本链接、采集时间、署名及许可分别保留，资料修改后仍能查原件。
    </p>
    <label
      >免费来源<select
        v-model="source"
        :disabled="busy || blocked"
        @change="changeSource"
      >
        <option v-for="item in sources" :key="item.id" :value="item.id">
          {{ item.label }}
        </option>
      </select></label
    >
    <p v-if="source === 'europe_pmc'" class="muted">
      请使用英语专业词。仅采集明确CC-BY或CC0许可的原文；机构收录和开放获取不表示结论已核实。
    </p>
    <p v-if="source === 'wikidata_names'" class="muted">
      输入姓氏或个人名线索，最多5条。采集时核对名字类型和指定语言标签；文化、年代、读音和流行度仍需核对。
    </p>
    <p v-if="source === 'official_reference'" class="muted">
      粘贴中国政府网、全国人大、最高法院、国家统计局、NIST、美国国家档案馆或国会图书馆公开文章链接。支持批准的正文路径；不跟随跳转，遵守robots。仅保存元数据、网页指纹与最多160字摘录；不能据此推定全文许可或法规当前效力。
    </p>
    <label
      >资料语言<select v-model="language" :disabled="busy || blocked">
        <option v-for="lang in languages" :key="lang" :value="lang">
          {{ languageNames[lang] }}
        </option>
      </select></label
    >
    <label
      >研究关键词<input
        v-model="query"
        :maxlength="source === 'official_reference' ? 2000 : 240"
        :disabled="busy || blocked"
        :placeholder="
          source === 'official_reference'
            ? 'https://www.gov.cn/zhengce/…'
            : '例如：宋代 泉州 市舶司 商船'
        "
        @keydown.enter.prevent="search"
    /></label>
    <button :disabled="busy || blocked || !query.trim()" @click="search">
      搜索公开资料
    </button>
    <p v-if="busy" role="status">正在读取公开资料…</p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="hits.length" class="muted">
      以下结果对应：{{ searched.query }} · {{ searched.language }}
    </p>
    <ul v-if="hits.length">
      <li v-for="hit in hits" :key="hit.page_id">
        <a :href="hit.url" target="_blank" rel="noopener noreferrer">{{
          hit.title
        }}</a>
        <p>{{ hit.snippet }}</p>
        <button :disabled="busy || blocked" @click="collect(hit)">
          采集并编辑：{{ hit.title }}
        </button>
      </li>
    </ul>
    <details>
      <summary>已采集资料（最近100项）</summary>
      <button :disabled="busy || blocked" @click="refresh().catch(fail)">
        刷新采集历史
      </button>
      <p v-if="!visibleHistory.length">暂无采集记录。</p>
      <ul>
        <li v-for="item in visibleHistory" :key="item.id">
          <button
            :disabled="busy || blocked"
            @click="emit('selected', item.id)"
          >
            编辑采集稿：{{ item.source.title }}
          </button>
          <span>
            · {{ researchSourceLabel(item.source.provider) }} ·
            {{
              item.source.source_version || `版本 ${item.source.revision_id}`
            }}
            · {{ item.source.captured_at }}</span
          >
          <a :href="item.source.url" target="_blank" rel="noopener noreferrer"
            >原始版本与作者记录</a
          >
        </li>
      </ul>
    </details>
  </details>
</template>
<style scoped>
.research-source-picker {
  margin: 20px 0;
  padding: 16px;
  border: 1px solid #d6e2d9;
  border-radius: 8px;
}
label {
  display: block;
  margin: 12px 0;
}
input,
select {
  display: block;
  width: 100%;
}
li {
  margin: 16px 0;
}
li a {
  margin-right: 12px;
}
</style>
