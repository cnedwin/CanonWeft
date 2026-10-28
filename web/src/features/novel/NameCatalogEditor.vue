<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import type { NameCatalog, NameMaterial } from "./characterTypes";
import ResearchSourcePicker from "../ip/ResearchSourcePicker.vue";
import NameSourceBatch from "./NameSourceBatch.vue";
const props = defineProps<{ root: string }>();
const emit = defineEmits<{ close: []; saved: [NameCatalog]; expired: [] }>();
const dialog = ref<HTMLDialogElement>();
const catalog = ref<NameCatalog>();
const baseline = ref("");
const busy = ref(false),
  sourceBusy = ref(false),
  error = ref(""),
  notice = ref("");
const search = ref(""),
  index = ref(-1),
  page = ref(0);
const kinds: Record<string, string> = {
  full_name: "完整姓名",
  surname: "姓氏",
  given_name: "个人名",
  courtesy_name: "字",
  art_name: "号",
  nickname: "昵称",
  title: "称号",
};
const fields: { key: keyof NameMaterial; label: string; limit: number }[] = [
  { key: "label", label: "名字素材", limit: 120 },
  { key: "label_language", label: "标签语言", limit: 30 },
  { key: "pronunciation", label: "读音", limit: 300 },
  { key: "original", label: "原文", limit: 300 },
  { key: "transliteration", label: "音译", limit: 300 },
  { key: "culture", label: "文化与地域", limit: 300 },
  { key: "era", label: "适用年代", limit: 300 },
  { key: "license", label: "许可或使用限制", limit: 300 },
  { key: "source_url", label: "来源网址", limit: 2000 },
];
let alive = true;
const dirty = computed(
  () =>
    !!catalog.value &&
    JSON.stringify(catalog.value.document) !== baseline.value,
);
const filtered = computed(() =>
  (catalog.value?.document.records ?? [])
    .map((r, i) => ({ r, i }))
    .filter(({ r }) =>
      [r.label, r.kind, r.culture, r.label_language, ...(r.tags ?? [])]
        .join(" ")
        .toLowerCase()
        .includes(search.value.toLowerCase()),
    ),
);
const visible = computed(() =>
  filtered.value.slice(page.value * 30, page.value * 30 + 30),
);
const current = computed(() => catalog.value?.document.records[index.value]);
function show(value: NameCatalog) {
  catalog.value = value;
  baseline.value = JSON.stringify(value.document);
  index.value = -1;
  page.value = 0;
}
async function perform(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await action();
  } catch (e) {
    if (alive) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (alive) busy.value = false;
  }
}
function close() {
  if (!dirty.value && !busy.value && !sourceBusy.value) {
    dialog.value?.close();
    emit("close");
  }
}
function unload(e: BeforeUnloadEvent) {
  if (dirty.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(() => {
  dialog.value?.showModal();
  window.addEventListener("beforeunload", unload);
  void perform(async () => {
    const value = await api<NameCatalog>(
      `${props.root}/name-catalog/open`,
      "POST",
      {},
    );
    if (alive) show(value);
  });
});
onBeforeUnmount(() => {
  alive = false;
  window.removeEventListener("beforeunload", unload);
});
function save() {
  void perform(async () => {
    const value = await api<NameCatalog>(
      `${props.root}/name-catalog`,
      "PUT",
      catalog.value,
    );
    if (alive) {
      show(value);
      notice.value =
        "名字素材已保存。后续生成会使用这个版本，旧任务需重新准备。";
      emit("saved", value);
    }
  });
}
function add() {
  if (!catalog.value) return;
  catalog.value.document.records.push({
    entity_id: newOperationID(),
    label: "新名字素材",
    label_language: "zh",
    kind: "full_name",
    source_url: "",
    source_row: 0,
    license: "unknown",
    review_state: "unverified",
  });
  index.value = catalog.value.document.records.length - 1;
}
function remove() {
  if (catalog.value && index.value >= 0) {
    catalog.value.document.records.splice(index.value, 1);
    index.value = -1;
  }
}
function setField(key: keyof NameMaterial, value: string) {
  if (!current.value) return;
  Object.assign(current.value, { [key]: value });
}
async function importFile(event: Event) {
  const input = event.target as HTMLInputElement,
    file = input.files?.[0];
  input.value = "";
  if (!file || dirty.value) return;
  await perform(async () => {
    if (file.size > 240 * 1024)
      throw new Error("请将素材JSON控制在240KB以内。");
    const value = await api<NameCatalog>(
      `${props.root}/name-catalog/import-preview`,
      "POST",
      { text: await file.text() },
    );
    if (alive) {
      catalog.value = value;
      index.value = -1;
      notice.value = "导入已预览，尚未保存。请核对新增记录后点击保存。";
    }
  });
}
function exportFile() {
  if (!catalog.value) return;
  const url = URL.createObjectURL(
    new Blob([JSON.stringify(catalog.value.document, null, 2)], {
      type: "application/json",
    }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = "名字素材库.json";
  link.click();
  URL.revokeObjectURL(url);
}
function previewSource(importID: string) {
  previewSources([importID]);
}
function previewSources(importIDs: string[]) {
  if (dirty.value || busy.value) {
    notice.value = "原件已采集。请先保存当前手改，再从采集历史选择预览。";
    return;
  }
  void perform(async () => {
    const value = await api<NameCatalog>(
      `${props.root}/name-catalog/sources-preview`,
      "POST",
      { import_ids: importIDs },
    );
    if (alive) {
      const previousCount = catalog.value?.document.records.length ?? 0;
      catalog.value = value;
      index.value = -1;
      notice.value =
        value.document.records.length > previousCount
          ? `在线名字已加入预览，尚未保存。新增${value.document.records.length - previousCount}条；可编辑后保存，旧记录保持原样。`
          : "这个条目的语言记录已存在，保留了已有编辑。";
    }
  });
}
</script>

<template>
  <dialog ref="dialog" aria-label="编辑名字素材库" @cancel.prevent="close">
    <header>
      <h2>名字素材库</h2>
      <button :disabled="busy || sourceBusy || dirty" @click="close">
        关闭素材库
      </button>
    </header>
    <p>
      本项目、当前分支的私人资料；素材不自动创建角色或成为故事正史。保存会保留上一版记录，作者编辑不等于来源已核实。
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="busy" role="status">正在处理名字素材…</p>
    <template v-if="catalog">
      <NameSourceBatch
        :root="root"
        :blocked="busy || dirty || sourceBusy"
        @preview="previewSources"
        @expired="emit('expired')"
      />
      <ResearchSourcePicker
        v-if="catalog.project_id && catalog.branch_id"
        :project-id="catalog.project_id"
        :branch-id="catalog.branch_id"
        :blocked="busy || dirty"
        purpose="names"
        @selected="previewSource"
        @blocking="sourceBusy = $event"
        @expired="emit('expired')"
      />
      <div class="actions">
        <button :disabled="busy || !dirty" @click="save">保存名字素材</button
        ><button
          :disabled="busy || !dirty"
          @click="
            catalog.document = JSON.parse(baseline);
            index = -1;
            notice = '';
          "
        >
          放弃未保存的素材修改</button
        ><button :disabled="busy" @click="add">新增名字素材</button
        ><button :disabled="busy" @click="exportFile">导出素材 JSON</button>
      </div>
      <label
        >导入素材 JSON（先预览，再保存）<input
          type="file"
          accept=".json,application/json"
          :disabled="busy || dirty"
          @change="importFile"
      /></label>
      <details>
        <summary>导入格式与范围</summary>
        <p>
          使用“导出素材 JSON”的格式：records 数组，每条有
          label、label_language、kind，entity_id 可省略。kind 支持
          full_name、surname、given_name、courtesy_name、art_name、nickname、title。来源网址、许可、文化、年代、原文和音译可独立填写。导入不会抓取网址，也不会覆盖同
          ID 的已有记录；未知许可请保留 unknown。
        </p>
      </details>
      <label
        >搜索素材<input
          v-model="search"
          aria-label="搜索库中名字素材"
          @input="page = 0"
      /></label>
      <p>
        {{ filtered.length }}条符合条件 / 共{{
          catalog.document.records.length
        }}条 · {{ dirty ? "有未保存修改" : "已保存" }} · 版本{{
          catalog.revision
        }}
      </p>
      <div class="layout">
        <section aria-label="素材列表">
          <button
            v-for="item in visible"
            :key="`${item.r.entity_id}-${item.r.label_language}`"
            :disabled="busy"
            @click="index = item.i"
          >
            {{ item.r.label }} · {{ kinds[item.r.kind] }} ·
            {{ item.r.label_language }}
          </button>
          <div class="actions">
            <button :disabled="page === 0" @click="page--">上一页素材</button
            ><button
              :disabled="(page + 1) * 30 >= filtered.length"
              @click="page++"
            >
              下一页素材
            </button>
          </div>
        </section>
        <fieldset v-if="current" :disabled="busy">
          <legend>编辑选中的素材</legend>
          <label
            >名字类型<select v-model="current.kind" aria-label="素材名字类型">
              <option v-for="(label, key) in kinds" :key="key" :value="key">
                {{ label }}
              </option>
            </select></label
          >
          <label v-for="f in fields" :key="f.key"
            >{{ f.label
            }}<input
              :aria-label="`素材${f.label}`"
              :value="current[f.key]"
              :maxlength="f.limit"
              @input="
                setField(f.key, ($event.target as HTMLInputElement).value)
              "
          /></label>
          <label
            >标签（顿号分隔）<input
              :value="current.tags?.join('、')"
              @input="
                current.tags = ($event.target as HTMLInputElement).value
                  .split('、')
                  .filter(Boolean)
              "
          /></label>
          <label
            >简短证据与使用说明<textarea
              v-model="current.evidence"
              maxlength="2000"
            />
          </label>
          <p>
            记录状态：{{ current.review_state || "unverified" }} ·
            {{
              current.captured_at || catalog.document.captured_at
            }}；语言标签不代表文化来源。
          </p>
          <button @click="remove">从当前素材库移除</button>
        </fieldset>
        <p v-else>选择一条素材进行编辑，或新增记录。</p>
      </div>
    </template>
  </dialog>
</template>

<style scoped>
dialog {
  width: min(1060px, 92vw);
  max-height: 88vh;
  overflow: auto;
  border: 1px solid #cedbcf;
  border-radius: 12px;
  padding: 1.5rem;
  background: #fffef9;
  color: #233d34;
}
dialog::backdrop {
  background: #12271b80;
}
header,
.actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.7rem;
  flex-wrap: wrap;
}
.layout {
  display: grid;
  grid-template-columns: 1fr 1.3fr;
  gap: 1.2rem;
  margin-top: 1rem;
}
.layout section > button {
  display: block;
  width: 100%;
  text-align: left;
  margin: 0.2rem 0;
}
label {
  display: block;
  margin: 0.6rem 0;
}
input,
textarea,
select {
  display: block;
  width: 100%;
  box-sizing: border-box;
  padding: 0.5rem;
  border: 1px solid #ccd6cd;
  border-radius: 5px;
}
button {
  padding: 0.6rem;
  border-radius: 5px;
  border: 1px solid #cedbcf;
  background: #f3f8f0;
  color: #183d30;
}
button:disabled {
  opacity: 0.5;
}
fieldset {
  border: 1px solid #ccd6cd;
}
@media (max-width: 720px) {
  .layout {
    grid-template-columns: 1fr;
  }
}
</style>
