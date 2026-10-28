<script setup lang="ts">
import { deltaKey, type BranchDelta, type DeltaOperation } from "./branchDelta";
const props = defineProps<{
  delta: BranchDelta;
  modelValue?: Record<string, string>;
  readonly?: boolean;
  disabled?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [Record<string, string>] }>();
const names: Record<string, string> = {
  add: "新增",
  replace: "修改",
  disable: "停用",
  remove: "删除",
};
const labels: Record<string, string> = {
  entity: "角色或实体",
  assertion: "设定断言",
  rule: "世界规则",
  event: "故事事件",
  mapping: "身份映射",
  name: "名称",
  description: "描述",
  aliases: "别名",
  status: "状态",
  profile: "能力设定",
  locked: "锁定状态",
  subject: "主体",
  predicate: "属性",
  value: "内容",
  object_id: "关系对象",
  kind: "类别",
  effective_from: "生效时间",
  effective_until: "结束时间",
  known_from: "获知时间",
  known_by: "知情者",
  visibility: "可见范围",
  recorded_at: "记录时间",
  source: "来源",
  holder_id: "持有人",
  context: "语境",
  confidence: "可信度",
  participants: "参与者",
  causes: "原因",
  time: "发生时间",
  requirements: "前置条件",
  effects: "效果",
  costs: "代价",
  limits: "限制",
  counters: "克制",
  growth: "成长",
  related: "关联",
  operator: "约束方式",
  operand: "约束值",
  when: "适用条件",
  exceptions: "例外条件",
  target: "目标",
  sources: "来源对象",
  reason: "理由",
  active: "启用",
  disabled: "停用",
  manual: "人工编辑",
  merge: "合并来源",
  promotion: "回流来源",
  previous: "原版本",
  server_timestamp: "系统记录",
};
function title(op: DeltaOperation) {
  const value = (op.after ?? op.before) as { name?: string } | null;
  return `${labels[op.object_kind] ?? "设定"}${value?.name ? `「${value.name}」` : ""}${op.field ? ` · ${labels[op.field.slice(1)] ?? "内容"}` : ""}`;
}
function describe(value: unknown): string {
  if (value === null) return "无";
  if (typeof value === "string") return labels[value] ?? value;
  if (typeof value === "boolean") return value ? "是" : "否";
  if (Array.isArray(value))
    return value.length ? value.map(describe).join("、") : "无";
  if (typeof value === "object")
    return Object.entries(value)
      .filter(([key]) => key !== "id")
      .map(([key, item]) => `${labels[key] ?? key}：${describe(item)}`)
      .join("\n");
  return String(value);
}
function update(key: string, event: Event) {
  emit("update:modelValue", {
    ...(props.modelValue ?? {}),
    [key]: (event.target as HTMLTextAreaElement).value,
  });
}
</script>
<template>
  <section class="panel" data-testid="branch-delta">
    <h4>逐项变更与理由</h4>
    <p v-if="!delta.operations.length" class="muted">当前没有内容差异。</p>
    <article v-for="op in delta.operations" :key="deltaKey(op)">
      <strong>{{ names[op.kind] ?? op.kind }} · {{ title(op) }}</strong>
      <div class="comparison">
        <div>
          <small>原内容</small>
          <pre>{{ describe(op.before) }}</pre>
        </div>
        <div>
          <small>新内容</small>
          <pre>{{ describe(op.after) }}</pre>
        </div>
      </div>
      <p v-if="readonly">理由：{{ op.reason }}</p>
      <label v-else
        >这项变化的理由<textarea
          :value="modelValue?.[deltaKey(op)] ?? ''"
          :aria-label="`变更理由 ${deltaKey(op)}`"
          :disabled="disabled"
          maxlength="4000"
          rows="2"
          required
          @input="update(deltaKey(op), $event)"
        ></textarea>
      </label>
      <p v-if="op.source_refs?.length" class="muted">
        来源：{{
          op.source_refs
            .map(
              (source) =>
                `${labels[source.kind] ?? "来源"} · ${source.revision.slice(0, 8)}`,
            )
            .join("；")
        }}
      </p>
    </article>
    <p class="muted">
      以上是确定的内容差异。下游影响和未登记的远端依赖仍需独立影响评估，不能据此认为其他内容都不受影响。
    </p>
  </section>
</template>
