<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { api, APIError, message } from "../../api";
import type { ChapterDraft, ChapterReference } from "./chapterTypes";
import type { DependencyRef } from "../ip/dependencies";

const props = defineProps<{
  root: string;
  projectId: string;
  draft: ChapterDraft;
  disabled: boolean;
  saved: boolean;
}>();
const emit = defineEmits<{
  change: [references: ChapterReference[]];
  expired: [];
}>();
interface Hit {
  source: DependencyRef;
  name: string;
  space: string;
  quote: string;
  can_use: boolean;
  reason?: string;
}
const query = ref("");
const hits = ref<Hit[]>([]);
const busy = ref(false);
const error = ref("");
const searched = ref(false);
const original = ref("");
const refs = computed(() => props.draft.document.references ?? []);
let disposed = false;
onBeforeUnmount(() => {
  disposed = true;
});
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function search() {
  if (busy.value || props.disabled || !props.saved || !query.value.trim())
    return;
  busy.value = true;
  error.value = "";
  hits.value = [];
  const hash = props.draft.hash;
  try {
    const result = await api<{ items: Hit[] }>(
      `${props.root}/research/search`,
      "POST",
      {
        query: query.value.trim(),
        revision: props.draft.revision,
        hash,
      },
    );
    if (disposed) return;
    if (hash !== props.draft.hash || !props.saved)
      throw new Error("章稿已修改，请重新检索本章资料。");
    hits.value = result.items;
    searched.value = true;
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function select(hit: Hit) {
  if (props.disabled || !hit.can_use) return;
  const existing = refs.value.find((r) => r.source.id === hit.source.id);
  if (existing?.source.hash === hit.source.hash) return;
  if (!existing && refs.value.length >= 12) return;
  const value: ChapterReference = {
    source: hit.source,
    quote: hit.quote,
    use: existing?.use ?? "",
    era: existing?.era ?? "",
    region: existing?.region ?? "",
    limitations: existing?.limitations ?? "",
  };
  emit(
    "change",
    existing
      ? refs.value.map((r) => (r === existing ? value : r))
      : [...refs.value, value],
  );
}
function edit(
  index: number,
  key: "quote" | "use" | "era" | "region" | "limitations",
  event: Event,
) {
  if (props.disabled) return;
  const value = (event.target as HTMLTextAreaElement).value;
  emit(
    "change",
    refs.value.map((r, i) => (i === index ? { ...r, [key]: value } : r)),
  );
}
function remove(index: number) {
  if (!props.disabled)
    emit(
      "change",
      refs.value.filter((_, i) => i !== index),
    );
}
async function readSource(source: DependencyRef) {
  if (busy.value) return;
  busy.value = true;
  original.value = "";
  error.value = "";
  try {
    const r = await api<{ text: string; head: string }>(
      `/projects/${props.projectId}/resources/${source.id}`,
    );
    if (disposed) return;
    if (r.head !== source.revision)
      throw new Error(
        "来源已换版。请重新检索并核对后选用新版；旧引用和正文已保留。",
      );
    original.value = r.text;
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
const fields = [
  ["quote", "原文短引"],
  ["use", "本章用途"],
  ["era", "适用年代"],
  ["region", "适用地域"],
  ["limitations", "适用限制与待核对处"],
] as const;
</script>

<template>
  <section class="chapter-research" aria-label="本章专业参考">
    <h3>本章专业参考</h3>
    <p class="muted">
      检索已入库资料，选择后填写用途、年代、地域和限制。引用将用于写作与独立审稿，不会自动成为角色已知事实。来源变化后须重新核对；手写正文仍可保存。
    </p>
    <label
      >查找本章资料<input
        v-model="query"
        maxlength="600"
        :disabled="disabled || busy"
        placeholder="例如：民国上海造纸工艺，哪些细节需要核实"
        @keydown.enter.prevent="search"
    /></label>
    <button
      :disabled="disabled || busy || !saved || !query.trim()"
      @click="search"
    >
      {{ busy ? "正在读取资料…" : "检索本章资料" }}
    </button>
    <p v-if="!saved" class="muted">等待章稿保存后即可检索。</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="searched && !hits.length && !busy">
      没有可用结果。可先在题材研究中采集、核对并采用参考资料。
    </p>
    <article v-for="hit in hits" :key="hit.source.id" class="reference-card">
      <strong>{{ hit.name }}</strong>
      <p>{{ hit.quote }}</p>
      <p v-if="!hit.can_use">{{ hit.reason }}</p>
      <button :disabled="busy" @click="readSource(hit.source)">
        查看资料原文
      </button>
      <button
        :disabled="
          disabled ||
          !hit.can_use ||
          refs.some(
            (r) =>
              r.source.id === hit.source.id &&
              r.source.hash === hit.source.hash,
          ) ||
          (refs.length >= 12 &&
            !refs.some((r) => r.source.id === hit.source.id))
        "
        @click="select(hit)"
      >
        {{
          refs.some((r) => r.source.id === hit.source.id)
            ? "改用此版本并重新核对"
            : "加入本章参考"
        }}
      </button>
    </article>
    <p>{{ refs.length }} / 12 项本章参考</p>
    <fieldset
      v-for="(reference, index) in refs"
      :key="reference.source.id"
      :disabled="disabled"
      class="reference-card"
    >
      <legend>参考 {{ index + 1 }}</legend>
      <small
        >来源 {{ reference.source.id }} · 版本
        {{ reference.source.revision }}</small
      >
      <label v-for="[key, label] in fields" :key="key"
        >{{ label
        }}<textarea
          :value="reference[key]"
          maxlength="2000"
          rows="2"
          @input="edit(index, key, $event)"
        />
      </label>
      <button :disabled="busy" @click="readSource(reference.source)">
        核对引用原文
      </button>
      <button @click="remove(index)">移除本章引用</button>
    </fieldset>
    <details v-if="original" open>
      <summary>参考原文</summary>
      <pre>{{ original }}</pre>
      <button @click="original = ''">收起原文</button>
    </details>
  </section>
</template>

<style scoped>
.chapter-research {
  margin-block: 1rem;
  padding: 1rem;
  border: 1px solid #ced6d5;
  border-radius: 0.6rem;
}
.reference-card {
  margin-block: 0.8rem;
  padding: 0.8rem;
  border: 1px solid #d7dddb;
  border-radius: 0.4rem;
}
small,
p {
  overflow-wrap: anywhere;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 24rem;
  overflow: auto;
}
button {
  margin: 0.3rem 0.5rem 0.3rem 0;
}
</style>
