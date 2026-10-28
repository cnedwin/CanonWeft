<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, APIError, message } from "../../api";
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
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    busy.value = false;
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
    accept(b);
    notice.value = b.enabled
      ? "费用门禁已开启；保存本身不发起调用。"
      : "预算已保存，付费调用保持关闭。";
  });
}
onMounted(() =>
  perform(async () => accept(await api<Budget>("/model-budget"))),
);
async function refreshTotals() {
  await perform(async () => {
    budget.value = await api<Budget>("/model-budget");
  });
}
defineExpose({ refreshTotals });
</script>
<template>
  <section class="panel">
    <h2>调用预算</h2>
    <p class="muted">
      工作室共用一份预算。请求发出前先占用预算；超时或结果未知时保留占用，不自动重发。此处保存不会充值或生成内容。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <template v-if="budget"
      ><p>
        已记账 {{ display(budget.spent_micros) }} {{ budget.currency }} ·
        请求占用 {{ display(budget.reserved_micros) }} {{ budget.currency }} ·
        可用
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
        {{ budget.currency }}
      </p>
      <form @submit.prevent="save">
        <fieldset :disabled="busy">
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
          <label
            ><input
              v-model="enabled"
              type="checkbox"
            />允许预算范围内的付费调用</label
          >
          <p class="muted">
            关闭会阻止新的请求。已发出的请求仍可能计费。服务商最终账单与本地估算存在差异时，需按回执核对。
          </p>
          <button>保存预算设置</button>
        </fieldset>
      </form>
    </template>
  </section>
</template>
