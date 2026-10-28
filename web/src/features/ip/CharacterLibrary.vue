<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import type { Snapshot } from "./types";
import CharacterImages from "./CharacterImages.vue";
import {
  characterFacts,
  characterFacets,
  characterFacetValues,
  characterFields,
  groupedCharacterFields,
  characterMatches,
  characterRelations,
  characterValues,
} from "./characterLibrary";

const props = defineProps<{
  snapshot: Snapshot;
  canEdit: boolean;
  disabled: boolean;
  lockedEntityIds: string[];
  visibleEntityIds?: string[];
  contextLabel?: string;
  projectId?: string;
  branchId?: string;
  branchHead?: string;
  canPublish?: boolean;
}>();
const searchInput = ref<HTMLInputElement | null>(null);
const detailHeading = ref<HTMLHeadingElement | null>(null);
function focusSearch() {
  searchInput.value?.focus();
}
defineExpose({ focusSearch });
const emit = defineEmits<{
  edited: [];
  editAssertion: [id: string];
  blocking: [value: boolean];
}>();
const selectionProblem = ref("");
const arrowID = `character-arrow-${crypto.randomUUID()}`;
const fieldListID = `character-fields-${crypto.randomUUID()}`;
const query = ref("");
const status = ref("all");
const facetFilters = ref({ faction: "", profession: "", tags: "" });
const relationType = ref("");
const relationPage = ref(1);
const zoom = ref(1);
const position = ref(0);
const at = computed(() =>
  Number.isSafeInteger(Number(position.value)) ? Number(position.value) : 0,
);
const page = ref(1);
const selectedId = ref("");
const newField = ref("性格");
const newValue = ref("");
const newValueInput = ref<HTMLTextAreaElement | null>(null);
const imageBlocking = ref(false);
const portraits = ref<Record<string, string>>({});
const portraitLoaded = ref<Record<string, boolean>>({});
const characterList = computed(() =>
  (props.snapshot.entities ?? []).filter(
    (e) =>
      e.kind === "character" &&
      (!props.visibleEntityIds || props.visibleEntityIds.includes(e.id)),
  ),
);
const facts = computed(() => characterFacts(props.snapshot, at.value));
const facets = computed(() =>
  characterFacets.map((facet) => ({
    ...facet,
    options: [
      ...new Set(
        characterList.value.flatMap((e) =>
          characterFacetValues(facts.value, e.id, facet),
        ),
      ),
    ].sort((a, b) => a.localeCompare(b, "zh-CN")),
  })),
);
const matches = computed(() =>
  characterList.value.filter(
    (e) =>
      (status.value === "all" ||
        (status.value === "disabled"
          ? e.status === "disabled"
          : e.status !== "disabled")) &&
      characterMatches(e, facts.value, query.value) &&
      characterFacets.every(
        (facet) =>
          !facetFilters.value[facet.key] ||
          characterFacetValues(facts.value, e.id, facet).includes(
            facetFilters.value[facet.key],
          ),
      ),
  ),
);
const pages = computed(() => Math.max(1, Math.ceil(matches.value.length / 24)));
const cards = computed(() =>
  matches.value.slice((page.value - 1) * 24, page.value * 24),
);
const selected = computed(() =>
  characterList.value.find((e) => e.id === selectedId.value),
);
const entityLocked = computed(() =>
  props.lockedEntityIds.includes(selectedId.value),
);
const relations = computed(() =>
  characterRelations(props.snapshot, at.value, selectedId.value),
);
const relationTypes = computed(() =>
  [...new Set(relations.value.map((r) => r.predicate))].sort(),
);
const filteredRelations = computed(() =>
  relations.value.filter(
    (r) => !relationType.value || r.predicate === relationType.value,
  ),
);
const neighbors = computed(() => {
  const ids = new Set(
    filteredRelations.value.flatMap((r) => [r.subject, r.object_id!]),
  );
  return characterList.value.filter(
    (e) => e.id !== selectedId.value && ids.has(e.id),
  );
});
const relationPages = computed(() =>
  Math.max(1, Math.ceil(neighbors.value.length / 12)),
);
const nodes = computed(() => {
  if (!selected.value) return [];
  const shown = neighbors.value.slice(
    (relationPage.value - 1) * 12,
    relationPage.value * 12,
  );
  return [
    { entity: selected.value, x: 450, y: 250 },
    ...shown.map((entity, index) => {
      const angle = (2 * Math.PI * index) / shown.length - Math.PI / 2;
      return {
        entity,
        x: 450 + 330 * Math.cos(angle),
        y: 250 + 200 * Math.sin(angle),
      };
    }),
  ];
});
const edges = computed(() =>
  nodes.value.slice(1).map((node) => {
    const associated = filteredRelations.value.filter(
      (r) => r.subject === node.entity.id || r.object_id === node.entity.id,
    );
    const length = Math.hypot(node.x - 450, node.y - 250);
    const dx = ((node.x - 450) * 38) / length;
    const dy = ((node.y - 250) * 38) / length;
    return {
      ...node,
      x1: 450 + dx,
      y1: 250 + dy,
      x2: node.x - dx,
      y2: node.y - dy,
      outgoing: associated.some((r) => r.subject === selectedId.value),
      incoming: associated.some((r) => r.object_id === selectedId.value),
      label: [...new Set(associated.map((r) => r.predicate))].join(" / "),
    };
  }),
);
const selectedAssertions = computed(() =>
  (props.snapshot.assertions ?? []).filter(
    (a) => a.subject === selectedId.value || a.object_id === selectedId.value,
  ),
);
const fieldGroups = computed(() =>
  groupedCharacterFields(facts.value, selectedId.value),
);
const typeNames: Record<string, string> = {
  fact: "事实",
  belief: "信念",
  claim: "陈述",
  intent: "意图",
  inference: "推测",
};
const contextNames: Record<string, string> = {
  reality: "现实",
  flashback: "倒叙",
  dream: "梦境",
  rumor: "传闻",
  hypothetical: "假设",
};
watch([query, status, at], () => {
  page.value = 1;
});
watch(
  facetFilters,
  () => {
    page.value = 1;
  },
  { deep: true },
);
watch([relationType, at], () => {
  relationPage.value = 1;
});
watch(selectedId, () => {
  relationType.value = "";
  relationPage.value = 1;
  zoom.value = 1;
});
watch(relationPages, (count) => {
  relationPage.value = Math.min(relationPage.value, count);
});
watch(pages, (count) => {
  page.value = Math.min(page.value, count);
});
watch(
  () => characterList.value.map((e) => e.id).join("\0"),
  () => {
    if (!selected.value) selectedId.value = "";
  },
);
watch([newValue, imageBlocking], ([value, images]) =>
  emit("blocking", !!value.trim() || images),
);
onUnmounted(() => emit("blocking", false));
function select(id: string) {
  if (
    id !== selectedId.value &&
    (newValue.value.trim() || imageBlocking.value)
  ) {
    selectionProblem.value =
      "请先保存当前属性或图片，或清空未保存的输入，再切换角色。";
    return;
  }
  selectionProblem.value = "";
  selectedId.value = id;
  void nextTick(() => {
    if (selectedId.value !== id) return;
    if (id) detailHeading.value?.focus();
    else focusSearch();
  });
}
function name(id: string) {
  return characterList.value.find((e) => e.id === id)?.name || id;
}
function clearFilters() {
  query.value = "";
  status.value = "all";
  facetFilters.value = { faction: "", profession: "", tags: "" };
}
function summary(id: string, fields: readonly string[]) {
  return (
    characterValues(facts.value, id, fields)
      .map((a) => a.value)
      .join(" / ") || "待补充"
  );
}
function addField() {
  if (
    !selected.value ||
    !props.canEdit ||
    props.disabled ||
    entityLocked.value ||
    !newValue.value.trim() ||
    !newField.value.trim()
  )
    return;
  props.snapshot.assertions.push({
    id: crypto.randomUUID(),
    subject: selected.value.id,
    predicate: newField.value.trim(),
    value: newValue.value.trim(),
    kind: "fact",
    context: "reality",
    effective_from: at.value,
    known_from: at.value,
    known_by: [],
    visibility: "public",
    recorded_at: "",
    locked: false,
    source: "作者输入",
  });
  newValue.value = "";
  selectionProblem.value = "";
  emit("edited");
}
function chooseMissingField(name: string) {
  if (!selected.value || !props.canEdit || props.disabled || entityLocked.value)
    return;
  if (newValue.value.trim() && newField.value !== name) {
    selectionProblem.value =
      "当前属性还有未加入的内容，请先加入草稿或清空，再补充其他属性。";
    newValueInput.value?.focus();
    return;
  }
  newField.value = name;
  selectionProblem.value = "";
  newValueInput.value?.focus();
}
</script>

<template>
  <section class="panel character-library" aria-label="角色库">
    <header class="library-header">
      <div>
        <p class="eyebrow">人物与关系</p>
        <h2>角色库</h2>
      </div>
      <span class="pill">{{ characterList.length }} 位角色</span>
    </header>
    <p class="muted">
      {{
        contextLabel ||
        (canEdit
          ? "正在查看工作草稿，修改会自动暂存；确认采用并发布后才进入正式设定。"
          : "正在查看当前分支的正式角色设定。")
      }}
    </p>
    <div class="library-controls">
      <label
        >搜索角色<input
          ref="searchInput"
          v-model="query"
          type="search"
          placeholder="姓名、别名、ID、职业或关键词；空格组合检索"
      /></label>
      <label
        >使用状态筛选<select v-model="status">
          <option value="all">全部角色</option>
          <option value="active">正常使用</option>
          <option value="disabled">已停用</option>
        </select></label
      >
      <label
        >关系与属性的故事位置<input
          v-model.number="position"
          type="number"
          step="1"
      /></label>
    </div>
    <div class="library-controls">
      <label v-for="facet in facets" :key="facet.key">
        {{ facet.name }}筛选
        <select
          v-model="facetFilters[facet.key]"
          :aria-label="`${facet.name}筛选`"
        >
          <option value="">全部{{ facet.name }}</option>
          <option
            v-if="
              facetFilters[facet.key] &&
              !facet.options.includes(facetFilters[facet.key])
            "
            :value="facetFilters[facet.key]"
          >
            {{ facetFilters[facet.key] }}（当前无匹配）
          </option>
          <option v-for="value in facet.options" :key="value" :value="value">
            {{ value }}
          </option>
        </select>
      </label>
    </div>
    <div class="library-filter-summary">
      <span role="status"
        >匹配 {{ matches.length }} / {{ characterList.length }} 位角色</span
      >
      <button type="button" class="quiet" @click="clearFilters">
        清除角色筛选
      </button>
    </div>
    <p class="muted">
      属性和关系按故事位置
      {{ at }} 展示。传闻、信念、争议及其他时段记录在详情中单列。
    </p>
    <p v-if="!characterList.length">
      还没有角色。可在下方编辑区新增角色，角色卡会随草稿更新。
    </p>
    <p v-else-if="!matches.length" role="status">
      没有匹配角色，请调整关键词或筛选。
    </p>
    <div class="character-grid">
      <article
        v-for="entity in cards"
        :key="entity.id"
        data-testid="character-card"
        class="character-card"
        :class="{ selected: selectedId === entity.id }"
      >
        <div class="card-heading">
          <img
            v-if="projectId && branchId"
            v-show="portraitLoaded[entity.id]"
            :src="
              portraits[entity.id] ||
              `/api/projects/${projectId}/branches/${branchId}/character-images/portrait/${encodeURIComponent(entity.id)}`
            "
            :alt="`${entity.name}头像`"
            class="character-portrait"
            @load="portraitLoaded[entity.id] = true"
            @error="portraitLoaded[entity.id] = false"
          />
          <span
            v-if="!portraitLoaded[entity.id]"
            class="character-monogram"
            aria-hidden="true"
            >{{ (entity.name || "?").slice(0, 1) }}</span
          >
          <div>
            <h3>
              <button
                type="button"
                class="character-link"
                :aria-label="`打开角色 ${entity.name || '未命名角色'} · ${entity.id}`"
                @click="select(entity.id)"
              >
                {{ entity.name || "未命名角色" }}
              </button>
            </h3>
            <small>{{ entity.aliases?.join(" · ") || "尚无别名" }}</small>
          </div>
        </div>
        <code class="character-id">{{ entity.id }}</code>
        <p class="card-description">
          {{
            characterValues(facts, entity.id, ["背景", "经历", "background"])
              .map((value) => value.value)
              .join(" / ") ||
            entity.description ||
            "背景待补充"
          }}
        </p>
        <dl class="card-facts">
          <template
            v-for="fields in [
              ['性别', 'gender'],
              ['年龄', '年龄段', 'age'],
              ['角色定位', '定位'],
              ['性格', 'personality'],
              ['阵营', '组织', 'faction'],
              ['职业', '身份', 'profession'],
              ['技能', '能力', 'skills'],
              ['服饰', '服装', 'outfit'],
              ['特色道具', '道具', 'props'],
            ]"
            :key="fields[0]"
          >
            <dt>{{ fields[0] }}</dt>
            <dd>{{ summary(entity.id, fields) }}</dd>
          </template>
        </dl>
        <button type="button" @click="select(entity.id)">
          查看资料、三视图与关系
        </button>
        <span v-if="entity.status === 'disabled'" class="pill"
          >已停用，保留历史关系</span
        >
      </article>
    </div>
    <nav v-if="pages > 1" class="card-pagination" aria-label="角色分页">
      <button type="button" :disabled="page <= 1" @click="page--">
        上一页角色</button
      ><span>{{ page }} / {{ pages }} · 匹配 {{ matches.length }} 位</span
      ><button type="button" :disabled="page >= pages" @click="page++">
        下一页角色
      </button>
    </nav>
    <section v-if="selected" class="character-detail" aria-label="角色详情">
      <header class="library-header">
        <h3 ref="detailHeading" tabindex="-1">
          {{ selected.name || "未命名角色" }} · 角色详情
        </h3>
        <button type="button" @click="select('')">收起角色详情</button>
      </header>
      <p v-if="selectionProblem" role="alert">{{ selectionProblem }}</p>
      <p>
        稳定角色 ID：<code>{{ selected.id }}</code>
      </p>
      <p v-if="entityLocked" role="status">
        角色已锁定。请在下方开启锁定设定修订并说明理由，才能编辑。
      </p>
      <fieldset
        v-if="canEdit"
        :disabled="disabled || entityLocked"
        class="character-edit"
        @input="emit('edited')"
        @change="emit('edited')"
      >
        <legend>编辑角色基础资料</legend>
        <label>角色姓名<input v-model="selected.name" maxlength="300" /></label>
        <label
          >角色别名（逗号分隔）<input
            :value="selected.aliases?.join('，')"
            @input="
              selected.aliases = ($event.target as HTMLInputElement).value
                .split(/[,，]/)
                .map((v) => v.trim())
                .filter(Boolean)
            "
        /></label>
        <label
          >角色背景与简介<textarea v-model="selected.description" rows="3" />
        </label>
      </fieldset>
      <p v-else>{{ selected.description || "背景待补充" }}</p>
      <CharacterImages
        v-if="projectId && branchId"
        :key="selected.id"
        :project-id="projectId"
        :branch-id="branchId"
        :branch-head="branchHead || ''"
        :character-id="selected.id"
        :can-edit="canEdit"
        :can-publish="!!canPublish"
        :disabled="disabled"
        @blocking="imageBlocking = $event"
        @portrait="portraits[selected.id] = $event"
      />
      <section
        v-for="group in fieldGroups"
        :key="group.name"
        :aria-label="group.name"
        class="character-field-group"
      >
        <h4>{{ group.name }}</h4>
        <dl class="detail-facts">
          <template v-for="field in group.fields" :key="field.name"
            ><dt>{{ field.name }}</dt>
            <dd>
              <template v-if="field.values.length"
                ><span
                  v-for="value in field.values"
                  :key="value.id"
                  class="trait-value"
                  >{{ value.value
                  }}<button
                    v-if="canEdit"
                    type="button"
                    class="quiet"
                    :disabled="disabled"
                    :aria-label="`编辑${field.name}记录 ${value.id}`"
                    @click="emit('editAssertion', value.id)"
                  >
                    查看 / 编辑
                  </button></span
                ></template
              ><button
                v-else-if="canEdit"
                type="button"
                class="quiet"
                :disabled="disabled || entityLocked"
                :aria-label="`补充${field.name}`"
                @click="chooseMissingField(field.name)"
              >
                待补充 · 点击填写</button
              ><template v-else>待补充</template>
            </dd></template
          >
        </dl>
      </section>
      <form
        v-if="canEdit"
        class="add-character-field"
        @submit.prevent="addField"
      >
        <fieldset :disabled="disabled || entityLocked">
          <legend>补充角色属性</legend>
          <label
            >新增角色属性<input
              v-model="newField"
              :list="fieldListID"
              maxlength="300"
              aria-label="新增角色属性"
              placeholder="选择常用属性，也可直接输入自定义名称"
            />
            <datalist :id="fieldListID">
              <option
                v-for="field in characterFields"
                :key="field[0]"
                :value="field[0]"
              >
                {{ field[0] }}
              </option>
            </datalist></label
          >
          <label
            >新增属性内容<textarea
              ref="newValueInput"
              v-model="newValue"
              maxlength="4000"
              rows="2"
            />
          </label>
          <p class="muted">
            加入当前角色草稿，作为从故事位置
            {{ at }}
            起生效的事实记录。秘密、传闻或不同生效时间请使用下方设定编辑区。
          </p>
          <button
            type="submit"
            :disabled="!newValue.trim() || !newField.trim()"
          >
            加入角色草稿
          </button>
          <button
            type="button"
            class="quiet"
            :disabled="!newValue"
            @click="
              newValue = '';
              selectionProblem = '';
            "
          >
            清空属性输入
          </button>
        </fieldset>
      </form>
      <h3>与 {{ selected.name || "此角色" }} 的关联</h3>
      <p v-if="!relations.length">
        此故事位置还没有登记角色关系；未登记不表示不存在关系。
      </p>
      <template v-else>
        <div class="relationship-controls">
          <label
            >关系类型筛选<select
              v-model="relationType"
              aria-label="关系类型筛选"
            >
              <option value="">全部关系</option>
              <option
                v-if="relationType && !relationTypes.includes(relationType)"
                :value="relationType"
              >
                {{ relationType }}（此时无关系）
              </option>
              <option v-for="kind in relationTypes" :key="kind" :value="kind">
                {{ kind }}
              </option>
            </select></label
          >
          <button
            type="button"
            :disabled="zoom <= 0.75"
            @click="zoom = Math.max(0.75, zoom - 0.25)"
          >
            缩小关系图
          </button>
          <span>{{ Math.round(zoom * 100) }}%</span>
          <button
            type="button"
            :disabled="zoom >= 2"
            @click="zoom = Math.min(2, zoom + 0.25)"
          >
            放大关系图
          </button>
          <button type="button" class="quiet" @click="zoom = 1">
            重置关系图缩放
          </button>
        </div>
        <p class="muted">
          当前筛选 {{ neighbors.length }} 位相邻角色、{{
            filteredRelations.length
          }}
          / {{ relations.length }}
          条有向关系。角色卡筛选不隐藏相邻角色；每组最多显示12位邻居，筛选后的全部关系列于下方。
        </p>
        <p v-if="!filteredRelations.length" role="status">
          此故事位置没有符合筛选的关系，可切换到全部关系。
        </p>
        <nav
          v-if="relationPages > 1"
          class="card-pagination"
          aria-label="关系图分页"
        >
          <button
            type="button"
            :disabled="relationPage <= 1"
            @click="relationPage--"
          >
            上一组关联角色
          </button>
          <span>第 {{ relationPage }} / {{ relationPages }} 组</span>
          <button
            type="button"
            :disabled="relationPage >= relationPages"
            @click="relationPage++"
          >
            下一组关联角色
          </button>
        </nav>
        <div class="relationship-viewport">
          <svg
            viewBox="0 0 900 500"
            role="group"
            aria-label="当前角色关系图"
            :style="{ width: `${zoom * 100}%`, minWidth: `${640 * zoom}px` }"
          >
            <defs>
              <marker
                :id="arrowID"
                viewBox="0 0 10 10"
                refX="8"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M 0 0 L 10 5 L 0 10 z" fill="#557b60" />
              </marker>
            </defs>
            <g
              v-for="edge in edges"
              :key="edge.entity.id"
              class="relation-edge"
            >
              <line
                :x1="edge.x1"
                :y1="edge.y1"
                :x2="edge.x2"
                :y2="edge.y2"
                :marker-start="edge.incoming ? `url(#${arrowID})` : undefined"
                :marker-end="edge.outgoing ? `url(#${arrowID})` : undefined"
              />
              <text
                :x="(450 + edge.x) / 2"
                :y="(250 + edge.y) / 2 - 8"
                text-anchor="middle"
              >
                {{ edge.label.slice(0, 16) }}
              </text>
              <title>{{ edge.label }}</title>
            </g>
            <g
              v-for="node in nodes"
              :key="node.entity.id"
              class="relation-node"
              :class="{ central: node.entity.id === selectedId }"
              role="button"
              tabindex="0"
              :aria-label="`查看角色 ${node.entity.name || '未命名角色'} · ${node.entity.id}`"
              @click="select(node.entity.id)"
              @keydown.enter="select(node.entity.id)"
              @keydown.space.prevent="select(node.entity.id)"
            >
              <circle :cx="node.x" :cy="node.y" r="34" />
              <text :x="node.x" :y="node.y + 5" text-anchor="middle">
                {{ (node.entity.name || "未命名").slice(0, 7) }}
              </text>
              <title>{{ node.entity.name }} · {{ node.entity.id }}</title>
            </g>
          </svg>
        </div>
        <ul class="relation-list">
          <li v-for="relation in filteredRelations" :key="relation.id">
            <strong
              >{{ name(relation.subject) }} →
              {{ name(relation.object_id!) }}</strong
            >
            · {{ relation.predicate }}：{{ relation.value
            }}<button
              v-if="canEdit"
              type="button"
              class="quiet"
              :disabled="disabled"
              @click="emit('editAssertion', relation.id)"
            >
              查看 / 编辑关系
            </button>
          </li>
        </ul>
      </template>
      <details>
        <summary>
          全部设定记录（{{ selectedAssertions.length }}，含其他时段与非事实）
        </summary>
        <ul>
          <li v-for="a in selectedAssertions" :key="a.id">
            {{ typeNames[a.kind] || a.kind }} ·
            {{ contextNames[a.context || "reality"] || a.context }} ·
            {{ a.predicate }}：{{ a.value }} · 生效 {{ a.effective_from }} 至
            {{ a.effective_until ?? "未限定"
            }}<span v-if="a.status === 'disputed'"> · 有争议</span
            ><span v-if="a.status === 'retracted'"> · 已撤回</span
            ><span v-if="a.locked"> · 已锁定</span
            ><button
              v-if="canEdit"
              type="button"
              class="quiet"
              :disabled="disabled"
              @click="emit('editAssertion', a.id)"
            >
              查看 / 编辑记录
            </button>
          </li>
        </ul>
      </details>
    </section>
  </section>
</template>

<style scoped>
.library-header,
.card-heading,
.card-pagination {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}
.library-header h2,
.library-header h3,
.card-heading h3 {
  margin: 0.2rem 0;
}
.eyebrow {
  font-size: 0.8rem;
  letter-spacing: 0.14em;
  color: #597264;
  margin: 0;
}
.library-controls {
  display: grid;
  grid-template-columns: 2fr 1fr 1fr;
  gap: 1rem;
}
.library-filter-summary,
.relationship-controls {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.75rem;
  margin: 0.8rem 0;
}
.character-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
  gap: 1rem;
  margin: 1.2rem 0;
}
.character-card {
  padding: 1rem;
  border: 1px solid #d4ded7;
  border-radius: 12px;
  background: #fafcf9;
  min-width: 0;
}
.character-card.selected {
  border-color: #3c795a;
  box-shadow: 0 0 0 1px #3c795a;
}
.card-heading {
  justify-content: flex-start;
}
.character-portrait {
  width: 48px;
  height: 48px;
  object-fit: cover;
  border-radius: 12px;
}
.character-monogram {
  display: grid;
  place-items: center;
  flex: none;
  width: 48px;
  height: 48px;
  border-radius: 12px;
  background: #dce9dd;
  color: #285e43;
  font-size: 1.4rem;
}
.character-link {
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  text-align: left;
  font-size: 1.1rem;
}
.character-link:hover {
  text-decoration: underline;
}
.character-id {
  display: block;
  font-size: 0.72rem;
  color: #5b665f;
  overflow-wrap: anywhere;
  margin: 0.7rem 0;
}
.card-description {
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
  white-space: pre-wrap;
}
.card-facts,
.detail-facts {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 0.45rem 0.8rem;
  font-size: 0.9rem;
}
.card-facts dt,
.detail-facts dt {
  color: #56675a;
}
.card-facts dd,
.detail-facts dd {
  margin: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
.card-facts dd {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.character-detail {
  border-top: 1px solid #d4ded7;
  padding-top: 1.2rem;
}
.character-field-group {
  margin-block: 1rem;
  padding: 0.8rem 1rem;
  border: 1px solid #d4ded7;
  border-radius: 12px;
}
.character-field-group h4 {
  margin: 0 0 0.8rem;
}
.character-edit {
  display: grid;
  gap: 0.8rem;
}
.trait-value {
  display: block;
  margin-bottom: 0.4rem;
}
.add-character-field {
  max-width: 700px;
}
.relationship-viewport {
  overflow: auto;
  background: #f7faf6;
  border: 1px solid #d4ded7;
  border-radius: 12px;
}
.relationship-viewport svg {
  display: block;
  width: 100%;
  min-width: 640px;
}
.relation-edge line {
  stroke: #8da794;
  stroke-width: 1.5;
}
.relation-edge text {
  fill: #405d49;
  font-size: 12px;
  paint-order: stroke;
  stroke: #f7faf6;
  stroke-width: 5px;
}
.relation-node {
  cursor: pointer;
}
.relation-node circle {
  fill: #e7eee5;
  stroke: #557b60;
  stroke-width: 2;
}
.relation-node.central circle {
  fill: #c9dec9;
}
.relation-node text {
  fill: #213d2a;
  font-size: 12px;
}
.relation-node:focus circle,
.relation-node:hover circle {
  stroke: #15482b;
  stroke-width: 4;
}
.relation-list {
  padding-left: 1.2rem;
}
.relation-list li {
  margin: 0.7rem 0;
}
.quiet {
  margin-left: 0.5rem;
}
button:focus-visible {
  outline: 2px solid #285e43;
  outline-offset: 3px;
}
@media (max-width: 700px) {
  .library-controls {
    grid-template-columns: 1fr;
  }
  .library-header {
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .character-grid {
    grid-template-columns: 1fr;
  }
  .detail-facts {
    grid-template-columns: 5.5rem minmax(0, 1fr);
  }
}
</style>
