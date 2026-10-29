<script setup lang="ts">
import { computed } from "vue";
import {
  setupExplanation,
  type ModelSetupReport,
  type SetupTarget,
} from "./modelSetup";
const props = defineProps<{
  report: ModelSetupReport;
  names: Record<string, string>;
  connections: { id: string; name: string }[];
}>();
const emit = defineEmits<{ locate: [target: SetupTarget] }>();
const coreRoles = ["architect", "planner", "writer", "bookkeeper", "critic"];
const core = computed(() =>
  coreRoles.flatMap((role) =>
    props.report.roles.filter((row) => row.role === role),
  ),
);
const others = computed(() =>
  props.report.roles.filter((row) => !coreRoles.includes(row.role)),
);
function timeLabel(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "时间无法识别" : date.toLocaleString();
}
function amount(value: number) {
  return (value / 1000000).toLocaleString(undefined, {
    maximumFractionDigits: 6,
  });
}
function connectionName(id?: string) {
  return props.connections.find((c) => c.id === id)?.name ?? "已绑定模型";
}
</script>

<template>
  <section class="panel setup-checklist" aria-labelledby="model-setup-heading">
    <h2 id="model-setup-heading">开写前配置检查</h2>
    <p class="muted">
      这是已保存配置的只读快照，不会调用模型。检查时间：{{
        timeLabel(report.checked_at)
      }}。
    </p>
    <p class="notice">
      {{
        report.network_disabled
          ? "模型联网已关闭：当前服务不会向模型供应商发送请求。"
          : "模型联网未被服务端禁用策略关闭；尚未验证连通性或账户额度。"
      }}{{ report.budget.enabled ? "预算开关已开启。" : "付费调用已关闭。" }}
    </p>
    <p v-if="!report.available" role="status">
      配置检查暂不可用；下方设置仍可编辑，请稍后刷新模型配置。
    </p>
    <template v-else>
      <div class="setup-budget">
        <p v-if="!report.budget.configured">
          预算尚未完整设置，请核对币种、累计总额和单次上限。
        </p>
        <p v-else>
          可用预算 {{ amount(report.budget.available_micros) }}
          {{ report.budget.currency }} · 单次上限
          {{ amount(report.budget.per_call_micros) }} {{ report.budget.currency
          }}{{
            report.budget.available_micros === 0 ? " · 可用预算为零" : ""
          }}。可用金额已扣除记账与请求占用，结果未知的请求仍保留占用。
        </p>
        <button type="button" @click="emit('locate', 'budget')">
          查看预算设置
        </button>
      </div>
      <h3>常用写作用途</h3>
      <p class="muted">
        按当前任务配置所需用途即可；下列完整性不等于 Agent 已通过验收。
      </p>
      <ul class="setup-roles">
        <li v-for="row in core" :key="row.role">
          <strong>{{ names[row.role] ?? row.role }}</strong
          >：{{ setupExplanation(row.code).text }}
          <span v-if="row.connection_id" class="muted">
            · {{ connectionName(row.connection_id) }} · 固定版本
            {{ row.bound_revision
            }}<template v-if="row.bound_revision !== row.current_revision"
              >（当前版本 {{ row.current_revision }}）</template
            ></span
          >
          <span v-if="row.expires_at" class="muted">
            · 价格有效至 {{ timeLabel(row.expires_at) }}</span
          >
          <button
            v-if="setupExplanation(row.code).target"
            type="button"
            :aria-label="`${names[row.role] ?? row.role}：去设置`"
            @click="emit('locate', setupExplanation(row.code).target!)"
          >
            去设置
          </button>
        </li>
      </ul>
      <details v-if="others.length">
        <summary>其他用途（{{ others.length }}）</summary>
        <ul class="setup-roles">
          <li v-for="row in others" :key="row.role">
            <strong>{{ names[row.role] ?? row.role }}</strong
            >：{{ setupExplanation(row.code).text }}
            <span v-if="row.connection_id" class="muted">
              · {{ connectionName(row.connection_id) }} · 固定版本
              {{ row.bound_revision
              }}<template v-if="row.bound_revision !== row.current_revision"
                >（当前版本 {{ row.current_revision }}）</template
              ></span
            >
            <span v-if="row.expires_at" class="muted">
              · 价格有效至 {{ timeLabel(row.expires_at) }}</span
            >
            <button
              v-if="setupExplanation(row.code).target"
              type="button"
              :aria-label="`${names[row.role] ?? row.role}：去设置`"
              @click="emit('locate', setupExplanation(row.code).target!)"
            >
              去设置
            </button>
          </li>
        </ul>
      </details>
    </template>
    <p class="muted">
      Key
      仅核对保存记录，不读取或展示密钥，不验证账户额度、网络可达性或生成质量。固定旧版可继续使用的前提包括
      Key
      未变且当前配置未停用；需要更新模型或价格时可在下方核对并重新绑定。更改价格或预算后请点击“刷新模型配置”更新清单。实际生成仍执行原有权限、规划、资格和费用检查。
    </p>
  </section>
</template>

<style scoped>
.setup-roles {
  padding-left: 1.2rem;
}
.setup-roles li {
  margin: 0.75rem 0;
  line-height: 1.7;
}
.setup-roles button {
  margin-left: 0.75rem;
}
.setup-budget {
  margin-bottom: 1rem;
}
summary {
  cursor: pointer;
}
</style>
