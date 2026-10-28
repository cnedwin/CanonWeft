<script setup lang="ts">
import { computed, ref } from "vue";
import { newOperationID, api, message } from "../../api";
import type { Branch, PromotionDraft } from "./types";
import BranchDeltaPanel from "./BranchDeltaPanel.vue";
import { activeReasons, type BranchDelta } from "./branchDelta";
const props = defineProps<{
  project: string;
  branch: Branch;
  branches: Branch[];
  blocked: boolean;
  draft?: PromotionDraft;
}>();
const emit = defineEmits<{
  created: [];
  blocking: [value: boolean];
  draft: [value: PromotionDraft];
}>();
const sourceID = ref(props.draft?.source_branch ?? "");
const source = ref<Branch | null>(null);
const selection = ref<{
  entities: string[];
  assertions: string[];
  rules: string[];
  events: string[];
}>({ entities: [], assertions: [], rules: [], events: [] });
const reason = ref("");
const error = ref("");
const notice = ref("");
const busy = ref(false);
const operation = ref<string>(newOperationID());
const delta = ref<BranchDelta | null>(null);
const deltaInput = ref("");
const changeReasons = ref<Record<string, string>>({});
const children = computed(() =>
  props.branches.filter((b) => b.parent_id === props.branch.id && b.head),
);
async function perform(work: () => Promise<void>) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  emit("blocking", true);
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
    emit("blocking", false);
  }
}
async function load() {
  await perform(async () => {
    source.value = await api<Branch>(
      `/projects/${props.project}/branches/${sourceID.value}`,
    );
    const saved =
      props.draft?.source_branch === source.value.id ? props.draft : undefined;
    selection.value = saved
      ? JSON.parse(JSON.stringify(saved.selection))
      : { entities: [], assertions: [], rules: [], events: [] };
    reason.value = saved?.reason ?? "";
    operation.value =
      saved?.source_head === source.value.head &&
      saved.base_head === props.branch.head
        ? saved.operation_id
        : newOperationID();
    delta.value = null;
    changeReasons.value = { ...saved?.change_reasons };
    if (saved)
      notice.value =
        saved.source_head === source.value.head &&
        saved.base_head === props.branch.head
          ? "已恢复回流选择和理由，请核对后继续。"
          : "来源或目标版本已变化；已保留原选择和理由，请对照当前内容重新确认。";
  });
}
async function propose() {
  const current = source.value;
  if (!current) return;
  await perform(async () => {
    const fingerprint = JSON.stringify([
      current.id,
      current.head,
      props.branch.head,
      selection.value,
    ]);
    if (!delta.value || deltaInput.value !== fingerprint) {
      delta.value = await api<BranchDelta>(
        `/projects/${props.project}/branches/${props.branch.id}/promotion-delta-preview`,
        "POST",
        {
          source_branch: current.id,
          source_head: current.head,
          base_head: props.branch.head,
          operation_id: operation.value,
          reason: reason.value,
          selection: selection.value,
        },
      );
      deltaInput.value = fingerprint;
      if (delta.value.operations.length) {
        notice.value = "已列出实际变化，请逐项填写理由，再生成回流候选。";
        return;
      }
    }
    await api(
      `/projects/${props.project}/branches/${props.branch.id}/promotions`,
      "POST",
      {
        source_branch: current.id,
        source_head: current.head,
        base_head: props.branch.head,
        operation_id: operation.value,
        reason: reason.value,
        selection: selection.value,
        change_reasons: activeReasons(delta.value, changeReasons.value),
      },
    );
    notice.value =
      "已生成上游候选，等待负责人确认采用；两个分支的正式版本均未改动。";
    emit("created");
  });
}
function edited() {
  operation.value = newOperationID();
  if (source.value)
    emit("draft", {
      source_branch: source.value.id,
      source_head: source.value.head,
      base_head: props.branch.head,
      operation_id: operation.value,
      selection: JSON.parse(JSON.stringify(selection.value)),
      reason: reason.value,
      change_reasons: { ...changeReasons.value },
    });
}
function inputEdited(event: Event) {
  // Checkbox v-model commits on change. Emitting its old value on input can
  // trigger a parent render before that change and undo the native toggle.
  if (
    event.target instanceof HTMLInputElement &&
    event.target.type === "checkbox"
  )
    return;
  edited();
}
</script>
<template>
  <section v-if="children.length" class="panel">
    <h2>从改编回流设定</h2>
    <p class="muted">
      逐项选择可复用的设定并说明理由。仅选中的条目进入本分支候选，身份映射不会自动回流。已有锁定内容仍须走专门修订。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <label
      >回流来源<select
        v-model="sourceID"
        :disabled="busy || blocked"
        aria-label="回流来源"
      >
        <option value="">请选择直接改编分支</option>
        <option v-for="b in children" :key="b.id" :value="b.id">
          {{ b.name }}
        </option>
      </select></label
    ><button :disabled="busy || blocked || !sourceID" @click="load">
      读取改编正式版本
    </button>
    <form
      v-if="source"
      @submit.prevent="propose"
      @change="edited"
      @input="inputEdited"
    >
      <fieldset :disabled="busy || blocked">
        <legend>来自 {{ source.name }} 的已发布内容</legend>
        <p>请选择语义设定。作品平衡参数和单次运行结果应留在原作品中。</p>
        <label
          v-for="e in source.snapshot.entities"
          :key="e.id"
          class="checkbox-label"
          ><input
            v-model="selection.entities"
            type="checkbox"
            :value="e.id"
            :aria-label="`回流实体 ${e.name}`"
          />实体：{{ e.name }} · {{ e.description
          }}<span v-if="branch.snapshot.entities.some((old) => old.id === e.id)"
            >（覆盖同身份条目）</span
          ></label
        >
        <label
          v-for="a in source.snapshot.assertions"
          :key="a.id"
          class="checkbox-label"
          ><input
            v-model="selection.assertions"
            type="checkbox"
            :value="a.id"
          />设定：{{
            source.snapshot.entities.find((e) => e.id === a.subject)?.name
          }}
          · {{ a.predicate }}：{{ a.value }}</label
        >
        <label
          v-for="r in source.snapshot.rules"
          :key="r.id"
          class="checkbox-label"
          ><input
            v-model="selection.rules"
            type="checkbox"
            :value="r.id"
          />规则：{{ r.name }} · {{ r.predicate }} {{ r.operand }}</label
        >
        <label
          v-for="e in source.snapshot.events"
          :key="e.id"
          class="checkbox-label"
          ><input
            v-model="selection.events"
            type="checkbox"
            :value="e.id"
          />事件：{{ e.name }} · {{ e.description }}</label
        >
        <label
          >回流理由<textarea
            v-model="reason"
            required
            maxlength="2000"
            rows="3"
          ></textarea>
        </label>
        <BranchDeltaPanel
          v-if="delta"
          v-model="changeReasons"
          @update:model-value="edited"
          :delta="delta"
          :disabled="busy || blocked"
        />
        <button>生成回流候选</button>
      </fieldset>
    </form>
  </section>
</template>
