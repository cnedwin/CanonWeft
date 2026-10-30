<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, APIError, message } from "../../api";
import { useModelReads } from "./modelReads";
const { read, active } = useModelReads();
interface Budget {
  currency_locked: boolean;
  currency: string;
  limit_micros: number;
  per_call_micros: number;
  spent_micros: number;
  reserved_micros: number;
  enabled: boolean;
  revision: number;
}
const emit = defineEmits<{ expired: [] }>();
const budget = ref<Budget | null>(null);
const currency = ref("USD");
const limit = ref("0");
const perCall = ref("0");
const enabled = ref(false);
const busy = ref(false);
const error = ref("");
const notice = ref("");
function display(value: number) {
  return (value / 1000000).toFixed(6).replace(/0+$/, "").replace(/\.$/, "");
}
function micros(text: string) {
  if (!/^\d{1,7}(\.\d{1,6})?$/.test(text))
    throw new Error("金额应为非负数，最多六位小数。");
  const [whole, fraction = ""] = text.split(".");
  const value =
    BigInt(whole ?? "0") * 1000000n + BigInt(fraction.padEnd(6, "0"));
  if (value > 1000000000000n) throw new Error("预算超出支持范围。");
  return Number(value);
}
function accept(b: Budget) {
  budget.value = b;
  currency.value = b.currency;
  limit.value = display(b.limit_micros);
  perCall.value = display(b.per_call_micros);
  enabled.value = b.enabled;
}
async function perform(work: () => Promise<void>) {
  if (busy.value || !active()) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (active()) {
      error.value = message(e);
      if (e instanceof APIError && [401, 403].includes(e.status))
        budget.value = null;
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (active()) busy.value = false;
  }
}
async function save() {
  await perform(async () => {
    const b = await api<Budget>("/model-budget", "PUT", {
      currency: currency.value,
      limit_micros: micros(limit.value),
      per_call_micros: micros(perCall.value),
      enabled: enabled.value,
      revision: budget.value?.revision ?? 0,
    });
    if (!active()) return;
    accept(b);
    notice.value = b.enabled
      ? "费用门禁已开启；保存本身不发起调用。"
      : "预算已保存，付费调用保持关闭。";
  });
}
async function reloadBudget() {
  await perform(async () => {
    const value = await read<Budget>("/model-budget");
    if (active()) accept(value);
  });
}
onMounted(reloadBudget);
async function refreshTotals() {
  await perform(async () => {
    const value = await read<Budget>("/model-budget");
    if (active()) budget.value = value;
  });
}
defineExpose({ refreshTotals });
</script>
<template>
  <section class="panel budget-panel" aria-label="调用预算">
    <header class="section-heading">
      <div>
        <p class="eyebrow">工作室费用</p>
        <h2>调用预算</h2>
      </div>
      <button
        type="button"
        class="quiet"
        :disabled="busy"
        @click="budget ? refreshTotals() : reloadBudget()"
      >
        {{ budget ? "刷新费用统计" : "重新加载预算" }}
      </button>
    </header>
    <p class="author-page-description">
      工作室共用一份预算。请求发出前先占用预算；超时或结果未知时保留占用，不自动重发。此处保存不会充值或生成内容。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <template v-if="budget">
      <div class="budget-saved-status">
        <strong class="pill">{{
          budget.enabled ? "付费调用已允许" : "付费调用已关闭"
        }}</strong>
        <span
          >已保存总额 {{ display(budget.limit_micros) }} {{ budget.currency }} ·
          单次上限 {{ display(budget.per_call_micros) }}
          {{ budget.currency }}</span
        >
      </div>
      <dl class="budget-summary" aria-label="已保存预算的费用统计">
        <div>
          <dt>已记账</dt>
          <dd>
            {{ display(budget.spent_micros) }}
            <small>{{ budget.currency }}</small>
          </dd>
        </div>
        <div>
          <dt>请求占用</dt>
          <dd>
            {{ display(budget.reserved_micros) }}
            <small>{{ budget.currency }}</small>
          </dd>
        </div>
        <div>
          <dt>当前可用</dt>
          <dd>
            {{
              display(
                Math.max(
                  0,
                  budget.limit_micros -
                    budget.spent_micros -
                    budget.reserved_micros,
                ),
              )
            }}
            <small>{{ budget.currency }}</small>
          </dd>
        </div>
      </dl>
      <p v-if="budget.reserved_micros > 0" class="budget-reservation-note">
        请求占用尚未结算，结果未知时仍会保留。可在下方请求账目中按服务商账单核对。
      </p>
      <form @submit.prevent="save">
        <fieldset :disabled="busy">
          <legend>编辑预算设置</legend>
          <p class="budget-full muted">
            下方为本页设置；保存后生效。刷新费用统计会保留你正在编辑的内容。
          </p>
          <label
            >记账币种<select
              v-model="currency"
              aria-label="记账币种"
              :disabled="budget.currency_locked"
            >
              <option>USD</option>
              <option>CNY</option>
            </select></label
          >
          <label
            >累计预算总额<input v-model="limit" inputmode="decimal" required
          /></label>
          <label
            >单次请求上限<input v-model="perCall" inputmode="decimal" required
          /></label>
          <label class="budget-permission budget-full"
            ><input
              v-model="enabled"
              type="checkbox"
            />允许预算范围内的付费调用</label
          >
          <p class="muted budget-full">
            关闭会阻止新的请求。已发出的请求仍可能计费。服务商最终账单与本地估算存在差异时，需按回执核对。
          </p>
          <div class="budget-full"><button>保存预算设置</button></div>
        </fieldset>
      </form>
    </template>
  </section>
</template>
<style scoped>
.budget-saved-status {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  color: #5d6e63;
  font-size: 13px;
}
.budget-summary {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  margin: 20px 0;
}
.budget-summary > div {
  padding: 18px;
  border: 1px solid #e1e5da;
  border-radius: 10px;
  background: #f5f6ef;
}
.budget-summary dt {
  color: #5d6e63;
  font-size: 13px;
}
.budget-summary dd {
  margin: 10px 0 0;
  font-size: 24px;
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}
.budget-summary small {
  font-size: 12px;
  font-weight: 400;
}
.budget-reservation-note {
  padding: 12px 16px;
  border-left: 3px solid #b5a26b;
  background: #f6f2e7;
  font-size: 13px;
}
fieldset {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 18px;
  border: 0;
  border-top: 1px solid #e1e5da;
  margin: 24px 0 0;
  padding: 20px 0 0;
  min-width: 0;
}
legend {
  padding: 0 12px 0 0;
  font-weight: 600;
}
.budget-full {
  grid-column: 1 / -1;
}
.budget-full p,
fieldset p {
  margin: 0;
}
.budget-permission {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 16px;
  border: 1px solid #d7ded5;
  border-radius: 8px;
}
.budget-permission input {
  width: auto;
  margin: 0;
}
@media (max-width: 680px) {
  .budget-summary,
  fieldset {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
