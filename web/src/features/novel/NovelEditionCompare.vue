<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { APIError, message } from "../../api";
import type { ManuscriptPreview, SavedEdition } from "./manuscriptTypes";
import {
  compareManuscripts,
  manuscriptChangeLabels,
} from "./manuscriptComparison";
import { useManuscriptReads } from "./useManuscriptReads";

const props = defineProps<{
  bookId: string;
  editions: SavedEdition[];
  disabled?: boolean;
  loading?: boolean;
  hasMore?: boolean;
  loadError?: string;
}>();
const emit = defineEmits<{ expired: []; more: []; refresh: [] }>();
const reads = useManuscriptReads(props.bookId);
const editions = computed(() =>
  props.editions.filter((item) => item.published),
);
const leftID = ref(""),
  rightID = ref("");
const busy = ref(false),
  error = ref(""),
  onlyChanges = ref(true);
const result = ref<{
  left: ManuscriptPreview;
  right: ManuscriptPreview;
  leftName: string;
  rightName: string;
} | null>(null);
const comparison = computed(() =>
  result.value
    ? compareManuscripts(result.value.left, result.value.right)
    : null,
);
const rows = computed(() =>
  (comparison.value?.rows ?? []).filter(
    (row) => !onlyChanges.value || row.changes.length > 0,
  ),
);
let request = 0;
function reset() {
  ++request;
  reads.cancel();
  busy.value = false;
  error.value = "";
  result.value = null;
}
watch([leftID, rightID], reset, { flush: "sync" });
watch(
  () => editions.value.map((item) => item.id).join("|"),
  () => {
    const ids = editions.value.map((item) => item.id);
    if (!ids.includes(leftID.value)) {
      leftID.value = ids[1] ?? ids[0] ?? "";
      reset();
    }
    if (!ids.includes(rightID.value) || rightID.value === leftID.value) {
      rightID.value = ids.find((id) => id !== leftID.value) ?? "";
      reset();
    }
  },
  { immediate: true, flush: "sync" },
);
watch(
  () => props.bookId,
  () => reset(),
  { flush: "sync" },
);

async function compare() {
  if (busy.value || props.disabled || editions.value.length < 2) return;
  const left = editions.value.find((item) => item.id === leftID.value),
    right = editions.value.find((item) => item.id === rightID.value);
  if (!left || !right || left.id === right.id) return;
  reset();
  const current = ++request;
  busy.value = true;
  try {
    const responses = await Promise.allSettled([
      reads.preview({ source: "edition", edition_id: left.id }),
      reads.preview({ source: "edition", edition_id: right.id }),
    ]);
    if (!reads.active() || current !== request) return;
    const failures = responses.filter(
      (item): item is PromiseRejectedResult => item.status === "rejected",
    );
    const denied = failures.find(
      (item) =>
        item.reason instanceof APIError &&
        [401, 403].includes(item.reason.status),
    );
    if (denied) throw denied.reason;
    if (failures[0]) throw failures[0].reason;
    const [before, after] = responses;
    if (before?.status !== "fulfilled" || after?.status !== "fulfilled")
      throw new Error("两个稿本未能完整读取，请重新比较。");
    if (
      before.value.source !== "edition" ||
      before.value.edition_id !== left.id ||
      after.value.source !== "edition" ||
      after.value.edition_id !== right.id
    )
      throw new Error("读取的稿本与所选版本不一致，请重新比较。");
    result.value = {
      left: before.value,
      right: after.value,
      leftName: `${left.name} · ${left.id.slice(0, 8)}`,
      rightName: `${right.name} · ${right.id.slice(0, 8)}`,
    };
  } catch (e) {
    if (!reads.active() || current !== request) return;
    result.value = null;
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (reads.active() && current === request) busy.value = false;
  }
}
function signed(value: number) {
  return `${value > 0 ? "+" : ""}${value.toLocaleString()}`;
}
</script>

<template>
  <section class="panel edition-compare" aria-label="成书稿本比较">
    <h2>比较两个成书稿本</h2>
    <p>
      选择已经保存的两个成书版本，查看目录、章节版本和字符数量的变化。点击比较后才读取，不会修改稿本或调用模型。
    </p>
    <p v-if="loadError" role="alert">
      成书版本列表：{{ loadError }}
      <button :disabled="disabled || loading || busy" @click="emit('refresh')">
        重新读取版本列表
      </button>
    </p>
    <p v-if="editions.length < 2 && !loadError" role="status">
      {{
        loading
          ? "正在读取已保存的成书版本…"
          : "需要两个已保存的成书版本。可先将正式采用稿保存为成书版本，再改稿并另存下一版。"
      }}
    </p>
    <div v-if="editions.length >= 2" class="comparison-controls">
      <label>
        原稿本
        <select v-model="leftID" aria-label="比较原稿本" :disabled="disabled">
          <option v-for="e in editions" :key="e.id" :value="e.id">
            {{ e.name }} · {{ e.title }} · {{ e.id.slice(0, 8) }}
          </option>
        </select>
      </label>
      <label>
        新稿本
        <select v-model="rightID" aria-label="比较新稿本" :disabled="disabled">
          <option v-for="e in editions" :key="e.id" :value="e.id">
            {{ e.name }} · {{ e.title }} · {{ e.id.slice(0, 8) }}
          </option>
        </select>
      </label>
      <button
        :disabled="
          disabled || busy || !leftID || !rightID || leftID === rightID
        "
        @click="compare"
      >
        {{ busy ? "正在比较…" : "比较这两个稿本" }}
      </button>
      <button v-if="busy" @click="reset">停止读取</button>
      <p v-if="leftID === rightID">请选择两个不同稿本。</p>
    </div>
    <button
      v-if="hasMore"
      :disabled="disabled || loading || busy"
      @click="emit('more')"
    >
      加载更多可比较稿本
    </button>
    <p v-if="error" role="alert">{{ error }}</p>
    <div v-if="result && comparison" aria-label="稿本比较结果">
      <h3>{{ result.leftName }} → {{ result.rightName }}</h3>
      <p>
        {{ comparison.changed }} 章有变化：新增 {{ comparison.added }} 章，移出
        {{ comparison.removed }} 章，章节版本变化
        {{ comparison.versions }} 章，次序变化
        {{ comparison.reordered }} 章。正文字符合计
        {{ result.left.characters.toLocaleString() }} →
        {{ result.right.characters.toLocaleString() }}（差
        {{ signed(comparison.charactersDelta) }}）。同一章可能有多项变化。
      </p>
      <p class="muted">
        章节版本变化也可能来自写作依据；字符数相同不能证明内容相同。这里只比较保存的目录与版本信息，不逐字读取或判断正文质量。
      </p>
      <label class="changes-toggle">
        <input v-model="onlyChanges" type="checkbox" />仅看变化章节
      </label>
      <p v-if="!rows.length" role="status">
        {{
          onlyChanges
            ? "没有章节目录或版本信息变化。"
            : "这两个稿本都没有章节。"
        }}
      </p>
      <div v-else class="comparison-table">
        <table>
          <caption>
            按新稿本目录排列，已移出的章节列在末尾
          </caption>
          <thead>
            <tr>
              <th scope="col">原稿本</th>
              <th scope="col">新稿本</th>
              <th scope="col">变化</th>
              <th scope="col">字符差</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.id">
              <td>
                <template v-if="row.left">
                  {{ row.leftPosition }}. {{ row.left.volume }} /
                  {{ row.left.title }}
                  <p>
                    {{
                      row.left.included
                        ? `${row.left.characters.toLocaleString()} 字符`
                        : "未纳入正文"
                    }}
                  </p>
                  <p v-if="row.left.warning">{{ row.left.warning }}</p>
                </template>
                <span v-else>原目录中没有此章</span>
              </td>
              <td>
                <template v-if="row.right">
                  {{ row.rightPosition }}. {{ row.right.volume }} /
                  {{ row.right.title }}
                  <p>
                    {{
                      row.right.included
                        ? `${row.right.characters.toLocaleString()} 字符`
                        : "未纳入正文"
                    }}
                  </p>
                  <p v-if="row.right.warning">{{ row.right.warning }}</p>
                </template>
                <span v-else>已移出新目录</span>
              </td>
              <td>
                {{
                  row.changes
                    .map((change) => manuscriptChangeLabels[change])
                    .join("、") || "无变化"
                }}
              </td>
              <td>{{ signed(row.charactersDelta) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>

<style scoped>
.comparison-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: 12px;
  margin-bottom: 12px;
}
.comparison-controls label {
  display: grid;
  gap: 6px;
  flex: 1 1 220px;
  min-width: 0;
}
.comparison-controls select {
  max-width: 100%;
}
.changes-toggle {
  display: flex;
  gap: 8px;
  align-items: center;
}
.changes-toggle input {
  width: auto;
}
.comparison-table {
  overflow-x: auto;
  margin-top: 16px;
}
table {
  width: 100%;
  border-collapse: collapse;
  min-width: 540px;
}
caption {
  text-align: left;
  margin-bottom: 8px;
}
th,
td {
  text-align: left;
  vertical-align: top;
  border-bottom: 1px solid var(--border, #ddd);
  padding: 10px;
  overflow-wrap: anywhere;
}
td p {
  margin: 6px 0 0;
}
</style>
