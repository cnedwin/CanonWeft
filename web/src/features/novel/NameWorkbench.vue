<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { api, message, newOperationID } from "../../api";
import NameCatalogEditor from "./NameCatalogEditor.vue";
import type {
  CharacterDesignDraft,
  NameCandidate,
  NamingBoard,
  NameCatalog,
} from "./characterTypes";
const props = defineProps<{
  draft: CharacterDesignDraft;
  root: string;
  disabled: boolean;
}>();
const emit = defineEmits<{
  update: [CharacterDesignDraft["document"]];
  assist: [];
  expired: [];
}>();
const query = ref("");
const favorites = ref(false);
const target = ref("");
const materialError = ref("");
const materialBusy = ref(false);
const catalogOpen = ref(false);
const kinds: Record<string, string> = {
  full_name: "完整姓名",
  surname: "姓氏",
  given_name: "个人名",
  courtesy_name: "字",
  art_name: "号",
  nickname: "昵称",
  title: "称号",
};
const partFields: {
  key: keyof NonNullable<NameCandidate["parts"]>;
  label: string;
}[] = [
  { key: "surname", label: "姓氏" },
  { key: "given_name", label: "名字" },
  { key: "courtesy_name", label: "字" },
  { key: "art_name", label: "号" },
  { key: "nickname", label: "昵称" },
  { key: "title", label: "称号" },
  { key: "original", label: "姓名原文" },
  { key: "transliteration", label: "姓名音译" },
];
function catalogSaved(value: NameCatalog) {
  change((b) => {
    b.catalog = {
      resource_id: value.resource_id,
      revision: value.revision,
      hash: value.hash,
    };
  });
  materials.value = undefined;
}
const materials = ref<{
  records: {
    entity_id: string;
    label: string;
    label_language: string;
    source_url: string;
    license?: string;
  }[];
  captured_at: string;
  license: string;
}>();
let alive = true;
onBeforeUnmount(() => {
  alive = false;
});
const board = computed(() => props.draft.document.naming);
const cards = computed(() =>
  props.draft.document.candidate.entities.filter((e) =>
    props.draft.document.ids.includes(e.id),
  ),
);
const visible = computed(() =>
  (board.value?.candidates ?? []).filter(
    (c) =>
      (!favorites.value || c.favorite) &&
      [c.name, c.pronunciation, ...c.tags, c.rationale]
        .join(" ")
        .toLowerCase()
        .includes(query.value.toLowerCase()),
  ),
);
function change(edit: (b: NamingBoard) => void) {
  if (props.disabled) return;
  const doc: CharacterDesignDraft["document"] = JSON.parse(
    JSON.stringify(props.draft.document),
  );
  doc.naming ??= {
    count: 20,
    culture: "",
    era: "",
    constraints: [],
    language: "zh",
    material_query: "",
    candidates: [],
    gaps: [],
    checks: [],
  };
  edit(doc.naming);
  emit("update", doc);
}
function field(
  key: "culture" | "era" | "material_query" | "language" | "name_kind",
  value: string,
) {
  change((b) => {
    b[key] = value;
  });
}
function editCandidate(id: string, values: Partial<NameCandidate>) {
  change((b) => {
    const c = b.candidates.find((v) => v.id === id);
    if (c) Object.assign(c, values);
  });
}
function useName(candidate: NameCandidate) {
  if (props.disabled) return;
  const doc: CharacterDesignDraft["document"] = JSON.parse(
    JSON.stringify(props.draft.document),
  );
  const id = target.value || cards.value[0]?.id;
  const entity = doc.candidate.entities.find((e) => e.id === id);
  if (
    !entity ||
    entity.locked ||
    doc.base.entities.some((e) => e.id === id && e.locked)
  )
    return;
  const kind = board.value?.name_kind || "full_name";
  const parts = { ...candidate.parts };
  if (kind !== "full_name") parts[kind as keyof typeof parts] = candidate.name;
  for (const f of partFields) {
    if (!parts[f.key]) continue;
    const existing = doc.candidate.assertions.find(
      (a) => a.subject === id && a.predicate === f.label,
    );
    if (existing?.locked && existing.value !== parts[f.key]) {
      materialError.value = `角色的${f.label}已锁定，请先在角色编辑区处理。`;
      return;
    }
    if (existing) existing.value = parts[f.key]!;
    else
      doc.candidate.assertions.push({
        id: newOperationID(),
        subject: id!,
        predicate: f.label,
        value: parts[f.key]!,
        kind: "fact",
        effective_from: 0,
        known_from: 0,
        known_by: [],
        visibility: "author",
        recorded_at: "",
        locked: false,
        source: "作者选用起名候选",
      });
  }
  if (kind === "full_name" && entity.name !== candidate.name) {
    if (
      !doc.base.entities.some((e) => e.id === id) &&
      entity.name.startsWith("待完善角色 ")
    ) {
      entity.name = candidate.name;
    } else {
      entity.aliases = [...new Set([...entity.aliases, entity.name])];
      entity.name = candidate.name;
    }
  }
  if (["nickname", "courtesy_name", "art_name", "title"].includes(kind))
    entity.aliases = [...new Set([...entity.aliases, candidate.name])];
  emit("update", doc);
}
function safeURL(value: string) {
  try {
    const url = new URL(value);
    return ["https:", "http:"].includes(url.protocol) ? value : undefined;
  } catch {
    return undefined;
  }
}
async function loadMaterials() {
  if (materialBusy.value || props.disabled) return;
  materialBusy.value = true;
  materialError.value = "";
  try {
    const value = await api<NonNullable<typeof materials.value>>(
      `${props.root}/name-materials?language=${encodeURIComponent(board.value?.language ?? "zh")}&q=${encodeURIComponent(board.value?.material_query ?? "")}`,
    );
    if (alive) materials.value = value;
  } catch (error) {
    if (alive) materialError.value = message(error);
  } finally {
    if (alive) materialBusy.value = false;
  }
}
</script>

<template>
  <section class="naming-panel" aria-label="角色起名工作台">
    <h3>角色起名工作台</h3>
    <p>
      按本批角色要求与题材起名。锁定、收藏和修改会随角色候选保存；选名不会直接改动正文。
    </p>
    <button v-if="!board" :disabled="disabled" @click="change(() => {})">
      开始风格起名
    </button>
    <template v-else>
      <fieldset :disabled="disabled">
        <legend>起名条件</legend>
        <label
          >目标名字类型<select
            aria-label="起名目标类型"
            :value="board.name_kind || 'full_name'"
            @change="
              field('name_kind', ($event.target as HTMLSelectElement).value)
            "
          >
            <option v-for="(label, key) in kinds" :key="key" :value="key">
              {{ label }}
            </option>
          </select></label
        >
        <div class="fields">
          <label
            >候选数量<input
              aria-label="名字候选数量"
              type="number"
              min="1"
              max="100"
              :value="board.count"
              @input="
                change(
                  (b) =>
                    (b.count = Number(
                      ($event.target as HTMLInputElement).value,
                    )),
                )
              "
          /></label>
          <label
            >语言文化与地域<input
              aria-label="起名文化"
              :value="board.culture"
              maxlength="4000"
              placeholder="如：北方边城、架空江湖"
              @input="
                field('culture', ($event.target as HTMLInputElement).value)
              "
          /></label>
          <label
            >年代与背景<input
              aria-label="起名年代"
              :value="board.era"
              maxlength="4000"
              placeholder="如：宋代、近未来、架空"
              @input="field('era', ($event.target as HTMLInputElement).value)"
          /></label>
        </div>
        <label
          >姓氏、辈分、长度、禁用字等要求（每行一项）<textarea
            aria-label="起名约束"
            :value="board.constraints.join('\n')"
            @input="
              change(
                (b) =>
                  (b.constraints = ($event.target as HTMLTextAreaElement).value
                    .split('\n')
                    .filter(Boolean)),
              )
            "
          />
        </label>
        <details>
          <summary>浏览预置姓氏素材</summary>
          <button :disabled="disabled" @click="catalogOpen = true">
            编辑名字素材库
          </button>
          <p>
            500条历史采集姓氏标签，按标签语言筛选；不是完整名字、文化归属或热门排名。选中的筛选条件会用于生成参考。
          </p>
          <label
            >标签语言<select
              aria-label="名字素材语言"
              :value="board.language"
              @change="
                field('language', ($event.target as HTMLSelectElement).value)
              "
            >
              <option value="">全部</option>
              <option value="zh">中文标签</option>
              <option value="en">英语标签</option>
              <option value="ja">日语标签</option>
              <option value="fr">法语标签</option>
              <option value="de">德语标签</option>
            </select></label
          >
          <label
            >姓氏关键词<input
              aria-label="名字素材关键词"
              :value="board.material_query"
              maxlength="120"
              @input="
                field(
                  'material_query',
                  ($event.target as HTMLInputElement).value,
                )
              "
          /></label>
          <button :disabled="materialBusy" @click="loadMaterials">
            查询姓氏素材
          </button>
          <p v-if="materialError" role="alert">{{ materialError }}</p>
          <template v-if="materials"
            ><p>
              {{ materials.records.length }}条 · {{ materials.license }} ·
              采集于{{ materials.captured_at }}；单条页面未重新核验。
            </p>
            <ul class="material-list">
              <li
                v-for="m in materials.records"
                :key="`${m.entity_id}-${m.label_language}`"
              >
                <a
                  :href="safeURL(m.source_url)"
                  target="_blank"
                  rel="noopener noreferrer"
                  >{{ m.label }}</a
                >
                · {{ m.label_language }} · {{ m.license || "许可待核实" }}
              </li>
            </ul>
          </template>
        </details>
        <button @click="emit('assist')">保存条件并让起名助手生成</button>
      </fieldset>
      <div class="filters">
        <label
          >搜索名字、读音或标签<input
            v-model="query"
            aria-label="筛选名字候选" /></label
        ><label><input v-model="favorites" type="checkbox" />只看收藏</label>
      </div>
      <label
        >选定名字用于<select
          v-model="target"
          aria-label="选名目标角色"
          :disabled="disabled"
        >
          <option value="">{{ cards[0]?.name ?? "请选择角色" }}</option>
          <option v-for="c in cards" :key="c.id" :value="c.id">
            {{ c.name }}
          </option>
        </select></label
      >
      <p v-if="!board.candidates.length">
        尚无名字候选。生成结果须先预览，再由你放入编辑区。
      </p>
      <div class="name-grid">
        <article
          v-for="c in visible"
          :key="c.id"
          class="name-card"
          :aria-label="`名字候选 ${c.name}`"
        >
          <label
            >名字<input
              :value="c.name"
              :disabled="disabled || c.locked"
              maxlength="120"
              @input="
                editCandidate(c.id, {
                  name: ($event.target as HTMLInputElement).value,
                  confidence: 'unverified',
                })
              "
          /></label>
          <label
            >读音／音译<input
              :value="c.pronunciation"
              :disabled="disabled || c.locked"
              maxlength="200"
              @input="
                editCandidate(c.id, {
                  pronunciation: ($event.target as HTMLInputElement).value,
                })
              "
          /></label>
          <label
            >标签（用顿号分隔）<input
              :value="c.tags.join('、')"
              :disabled="disabled || c.locked"
              @input="
                editCandidate(c.id, {
                  tags: ($event.target as HTMLInputElement).value
                    .split('、')
                    .filter(Boolean),
                })
              "
          /></label>
          <label
            >适合角色的理由<textarea
              :value="c.rationale"
              :disabled="disabled || c.locked"
              maxlength="4000"
              @input="
                editCandidate(c.id, {
                  rationale: ($event.target as HTMLTextAreaElement).value,
                })
              "
            />
          </label>
          <p>
            {{
              {
                invented: "原创候选",
                collected: "采集素材",
                adapted: "素材改造",
              }[c.origin]
            }}
            ·
            {{
              c.confidence === "supported"
                ? "有来源材料，仍需核对"
                : "待作者核对"
            }}
          </p>
          <p v-for="conflict in c.conflicts" :key="conflict" class="warning">
            {{ conflict }}
          </p>
          <details v-if="c.sources.length">
            <summary>查看来源</summary>
            <p v-for="source in c.sources" :key="source.source_id">
              <a
                :href="safeURL(source.url)"
                target="_blank"
                rel="noopener noreferrer"
                >{{ source.evidence }}</a
              ><br />{{ source.locator }} · {{ source.captured_at }}
            </p>
          </details>
          <details>
            <summary>姓、名、字、号及原文</summary>
            <label v-for="f in partFields" :key="f.key"
              >{{ f.label
              }}<input
                :aria-label="`候选${f.label}`"
                :value="c.parts?.[f.key] || ''"
                :disabled="disabled || c.locked"
                maxlength="200"
                @input="
                  editCandidate(c.id, {
                    parts: {
                      ...c.parts,
                      [f.key]: ($event.target as HTMLInputElement).value,
                    },
                  })
                "
            /></label>
          </details>
          <div class="actions">
            <label
              ><input
                type="checkbox"
                :checked="c.locked"
                :disabled="disabled"
                @change="
                  editCandidate(c.id, {
                    locked: ($event.target as HTMLInputElement).checked,
                  })
                "
              />锁定</label
            ><label
              ><input
                type="checkbox"
                :checked="c.favorite"
                :disabled="disabled"
                @change="
                  editCandidate(c.id, {
                    favorite: ($event.target as HTMLInputElement).checked,
                  })
                "
              />收藏</label
            ><button :disabled="disabled || !c.name.trim()" @click="useName(c)">
              用于选定角色
            </button>
          </div>
        </article>
      </div>
      <p v-for="gap in board.gaps" :key="gap" class="warning">{{ gap }}</p>
      <details v-if="board.checks.length">
        <summary>检查范围</summary>
        <p v-for="check in board.checks" :key="check">{{ check }}</p>
      </details>
    </template>
    <NameCatalogEditor
      v-if="catalogOpen"
      :root="root"
      @close="catalogOpen = false"
      @saved="catalogSaved"
      @expired="emit('expired')"
    />
  </section>
</template>

<style scoped>
.naming-panel {
  margin: 1.2rem 0;
  padding: 1.2rem;
  border: 1px solid #dce4db;
  border-radius: 12px;
  background: #fafbf8;
}
.fields,
.name-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 1rem;
}
.name-card {
  border: 1px solid #dce4db;
  background: white;
  border-radius: 10px;
  padding: 1rem;
}
.name-card label,
.fields label {
  display: block;
}
.actions,
.filters {
  display: flex;
  align-items: center;
  gap: 1rem;
  flex-wrap: wrap;
}
.actions input,
.filters input[type="checkbox"] {
  width: auto;
}
.material-list {
  max-height: 240px;
  overflow: auto;
  columns: 3;
}
.warning {
  color: #865c20;
}
input,
textarea,
select {
  max-width: 100%;
  box-sizing: border-box;
}
fieldset {
  border: 1px solid #dce4db;
}
button {
  margin: 0.4rem 0;
}
</style>
