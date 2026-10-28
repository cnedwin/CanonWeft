<script setup lang="ts">
import { computed, onMounted, ref, onBeforeUnmount } from "vue";
import { api, message, type Actor, type Project } from "../../api";
import type { Branch, Candidate } from "../ip/types";
import type { NovelDraft } from "./types";
import BundlePanel from "../ip/BundlePanel.vue";
import GenerationPanel from "../ip/GenerationPanel.vue";
const props = defineProps<{ draft: NovelDraft }>();
const emit = defineEmits<{ close: []; adopted: [NovelDraft] }>();
type Person = {
  name: string;
  identity: string;
  goal: string;
  background: string;
  personality: string;
};
type Field = {
  field: string;
  value: string | number | Person[];
  basis: string;
  source_ids: string[];
  reason: string;
};
type Suggestion = {
  generation_id: string;
  revision: number;
  fields: Field[];
  warnings: string[];
};
const workspace = ref<{ project: Project; actor: Actor; branch: Branch }>(),
  sources = ref<
    {
      id: string;
      name: string;
      branch_id: string;
      head: string;
      kind: string;
    }[]
  >([]),
  candidates = ref<Candidate[]>([]);
const chosen = ref<string[]>([]),
  notes = ref(props.draft.setup.idea),
  busy = ref(false),
  error = ref(""),
  materials = ref(false),
  materialBlocked = ref(false),
  agentBlocked = ref(false),
  agentOpen = ref(false),
  suggestion = ref<Suggestion>(),
  selected = ref<string[]>([]);
const root = computed(() => `/novel-setups/${props.draft.id}`);
const labels: Record<string, string> = {
  title: "书名",
  genre: "题材",
  language: "语言",
  idea: "故事构思",
  conflict: "核心冲突",
  audience: "目标读者",
  world: "世界设定",
  theme: "主题",
  include: "必须包含",
  avoid: "需要避免",
  perspective: "叙事视角",
  style: "文风",
  target_words: "目标总字数",
  volume_count: "预计卷数",
  chapter_min: "单章最少字数",
  chapter_max: "单章最多字数",
  characters: "初始角色",
};
const basis: Record<string, string> = {
  user: "作者要求",
  source: "资料依据",
  proposal: "助手提议",
};
const personLabels: Record<keyof Person, string> = {
  name: "姓名",
  identity: "身份",
  goal: "目标",
  background: "背景",
  personality: "性格",
};
const payload = ref<Record<string, unknown>>({});
let alive = true;
async function refresh() {
  const w = workspace.value;
  if (!w) return;
  const [r, c, b] = await Promise.all([
    api<{ items: typeof sources.value }>(
      `/projects/${w.project.id}/resources?branch=${encodeURIComponent(w.branch.id)}`,
    ),
    api<{ items: Candidate[] }>(
      `/projects/${w.project.id}/branches/${w.branch.id}/candidates`,
    ),
    api<Branch>(`/projects/${w.project.id}/branches/${w.branch.id}`),
  ]);
  if (!alive) return;
  sources.value = r.items.filter(
    (i) =>
      i.branch_id === w.branch.id &&
      !!i.head &&
      !i.kind.startsWith("novel_plan_"),
  );
  candidates.value = c.items;
  w.branch = b;
}
onMounted(async () => {
  busy.value = true;
  try {
    const w = await api<typeof workspace.value>(
      `${root.value}/assistant`,
      "POST",
      { revision: props.draft.revision },
    );
    if (!alive) return;
    workspace.value = w;
    await refresh();
  } catch (e) {
    if (alive) error.value = message(e);
  } finally {
    if (alive) busy.value = false;
  }
});
onBeforeUnmount(() => {
  alive = false;
});
function openAgent() {
  if (!notes.value.trim()) {
    error.value = "写一句想法或说明希望完善哪些内容。";
    return;
  }
  payload.value = {
    setup_id: props.draft.id,
    revision: props.draft.revision,
    source_ids: [...chosen.value],
  };
  agentOpen.value = true;
  error.value = "";
}
function show(value: unknown) {
  const v = value as Suggestion;
  suggestion.value = JSON.parse(JSON.stringify(v));
  selected.value = v.fields.map((f) => f.field);
}
function oldValue(field: string) {
  const v = (props.draft.setup as unknown as Record<string, unknown>)[field];
  if (Array.isArray(v))
    return (
      v.map((p) => `${p.name}：${p.identity}；${p.goal}`).join("\n") ||
      "尚未填写"
    );
  return String(v ?? "") || "尚未填写";
}
async function adopt() {
  if (!suggestion.value || busy.value || !selected.value.length) return;
  busy.value = true;
  error.value = "";
  try {
    const result = await api<NovelDraft>(
      `${root.value}/assist-adoption`,
      "POST",
      {
        generation_id: suggestion.value.generation_id,
        revision: props.draft.revision,
        fields: suggestion.value.fields.filter((f) =>
          selected.value.includes(f.field),
        ),
      },
    );
    if (alive) emit("adopted", result);
  } catch (e) {
    if (alive) error.value = message(e);
  } finally {
    if (alive) busy.value = false;
  }
}
</script>
<template>
  <section class="setup-assistant">
    <header>
      <h2>从想法与资料完善建书内容</h2>
      <button
        type="button"
        :disabled="busy || materialBlocked || agentBlocked"
        @click="emit('close')"
      >
        返回开书方案
      </button>
    </header>
    <p>
      可以先写一句想法或上传资料。助手给出逐项建议，你可以编辑并选择采用；采用只更新私人草稿，最后仍需确认创建小说。
    </p>
    <p v-if="busy" role="status">正在保存或准备资料…</p>
    <template v-if="workspace">
      <template v-if="!materials && !agentOpen">
        <label
          >我的想法与完善要求<textarea
            v-model="notes"
            rows="5"
            maxlength="10000"
            placeholder="例如：想写一位修钟师寻找失踪朋友的都市悬疑小说，帮我完善书名、核心冲突和主角。"
          />
        </label>
        <button type="button" @click="materials = true">
          上传与整理参考资料
        </button>
        <fieldset>
          <legend>让助手阅读的资料（最多12份）</legend>
          <label v-for="source in sources" :key="source.id" class="choice"
            ><input v-model="chosen" type="checkbox" :value="source.id" />{{
              source.name
            }}</label
          >
          <p v-if="!sources.length">
            还没有已采用的资料，可以先上传、核对并采用，也可以只用文字想法。
          </p>
        </fieldset>
        <button
          type="button"
          :disabled="busy || chosen.length > 12"
          @click="openAgent"
        >
          打开建书助手
        </button>
      </template>
      <section v-if="materials" aria-label="建书前参考资料">
        <button
          type="button"
          :disabled="materialBlocked"
          @click="
            materials = false;
            refresh();
          "
        >
          资料已整理，返回完善要求
        </button>
        <BundlePanel
          :project="workspace.project"
          :actor="workspace.actor"
          :branch="workspace.branch"
          :candidates="candidates"
          :blocked="busy"
          initially-open-imports
          @blocking="materialBlocked = $event"
          @published="refresh"
        />
      </section>
      <template v-if="agentOpen && !materials">
        <GenerationPanel
          :project="workspace.project"
          :actor="workspace.actor"
          :branch="workspace.branch"
          :assistance="{
            root,
            kind: 'novel_setup',
            role: 'architect',
            title: '建书内容助手',
            payload,
            notes,
          }"
          @blocking="agentBlocked = $event"
          @assistance-suggestion="show"
        />
        <button
          type="button"
          :disabled="agentBlocked || busy"
          @click="agentOpen = false"
        >
          返回想法与资料选择
        </button>
        <section v-if="suggestion" aria-label="建书字段建议">
          <h3>选择、编辑后采用</h3>
          <p
            v-for="(warning, i) in suggestion.warnings"
            :key="i"
            class="notice"
          >
            {{ warning }}
          </p>
          <article v-for="field in suggestion.fields" :key="field.field">
            <label class="choice"
              ><input
                v-model="selected"
                type="checkbox"
                :value="field.field"
              />采用{{ labels[field.field] ?? field.field }}</label
            >
            <p>{{ basis[field.basis] ?? field.basis }} · {{ field.reason }}</p>
            <p v-if="field.source_ids.length">
              来源：{{
                field.source_ids
                  .map(
                    (id) =>
                      sources.find((s) => s.id === id)?.name ?? "已固定资料",
                  )
                  .join("、")
              }}
            </p>
            <details>
              <summary>当前内容</summary>
              <p class="preserve">{{ oldValue(field.field) }}</p>
            </details>
            <template v-if="Array.isArray(field.value)"
              ><fieldset v-for="(person, i) in field.value" :key="i">
                <legend>角色 {{ i + 1 }}</legend>
                <label v-for="(label, key) in personLabels" :key="key"
                  >{{ label }}<textarea v-model="person[key]" rows="2" /></label
                ><button type="button" @click="field.value.splice(i, 1)">
                  移除此候选角色
                </button>
              </fieldset></template
            >
            <label v-else-if="typeof field.value === 'number'"
              >{{ labels[field.field] }}建议<input
                v-model.number="field.value"
                type="number"
                min="0"
                step="1"
            /></label>
            <label v-else
              >{{ labels[field.field] }}建议<textarea
                v-model="field.value"
                rows="4"
              />
            </label>
          </article>
          <button
            type="button"
            :disabled="busy || !selected.length"
            @click="adopt"
          >
            采用所选 {{ selected.length }} 项到建书草稿
          </button>
        </section>
      </template>
    </template>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>
</template>
<style scoped>
header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}
label {
  display: block;
  margin: 0.75rem 0;
}
.choice {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.choice input {
  width: auto;
  margin: 0;
  flex: 0 0 auto;
}
article {
  border: 1px solid var(--border, #ddd);
  border-radius: 8px;
  padding: 1rem;
  margin: 1rem 0;
}
.preserve {
  white-space: pre-wrap;
}
button {
  margin: 0.5rem 0.5rem 0.5rem 0;
}
textarea {
  width: 100%;
  box-sizing: border-box;
}
</style>
