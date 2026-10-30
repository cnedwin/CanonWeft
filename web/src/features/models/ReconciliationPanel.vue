<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { newOperationID, api, APIError, message } from "../../api";
import { useModelReads } from "./modelReads";
const { read } = useModelReads();
interface Call {
  id: string;
  state: string;
  project_name: string;
  provider: string;
  model: string;
  currency: string;
  revision: number;
  charged_micros: number;
  reserved_micros: number;
  provider_receipt: string;
  receipt_count: number;
  can_reconcile: boolean;
}
interface Adjustment {
  id: string;
  revision: number;
  previous_amount_micros: number;
  released_micros: number;
  retention_state?: "EXPIRED";
  expired_at?: string;
  input: {
    amount_micros: number;
    currency: string;
    bill_reference: string;
    evidence: string;
    reason: string;
  };
}
interface ExpiredReceipt {
  retention_state: "EXPIRED";
  id: string;
  outcome: string;
  amount_micros: number;
  expired_at: string;
}
interface History {
  adjustments: Adjustment[];
  receipts: ([string, number, string, string] | ExpiredReceipt)[];
}
const emit = defineEmits<{
  expired: [];
  blocking: [boolean];
  recorded: [];
  attention: [value: { count: number; loaded: number; hasMore: boolean }];
}>();
const calls = ref<Call[]>([]);
const current = ref<Call | null>(null);
const history = ref<History>({ adjustments: [], receipts: [] });
const next = ref("");
const amount = ref("");
const bill = ref("");
const evidence = ref("");
const reason = ref("");
const confirmed = ref(false);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const operation = ref(newOperationID());
const query = ref("");
const stateFilter = ref("");
const unknownCalls = computed(() =>
  calls.value.filter((call) => call.state === "UNKNOWN"),
);
const filteredCalls = computed(() => {
  const text = query.value.trim().toLocaleLowerCase();
  return calls.value.filter(
    (call) =>
      (!stateFilter.value || call.state === stateFilter.value) &&
      (!text ||
        [
          call.project_name,
          call.provider,
          call.model,
          call.id,
          names[call.state] ?? call.state,
        ].some((value) => value.toLocaleLowerCase().includes(text))),
  );
});
watch(
  [calls, next],
  () =>
    emit("attention", {
      count: unknownCalls.value.length,
      loaded: calls.value.length,
      hasMore: !!next.value,
    }),
  { immediate: true },
);
const dirty = computed(
  () =>
    !!(bill.value || evidence.value || reason.value || confirmed.value) ||
    (current.value !== null &&
      amount.value !== money(current.value.charged_micros)),
);
watch([amount, bill, evidence, reason, confirmed], () => {
  operation.value = newOperationID();
});
watch([dirty, busy], () => emit("blocking", dirty.value || busy.value));
let disposed = false;
const names: Record<string, string> = {
  RESERVED: "尚未发送",
  DISPATCHED: "请求已发送",
  UNKNOWN: "结果未知，费用占用中",
  RECONCILED: "账单已核对",
  SUCCEEDED: "已收到结果",
  INVALID_OUTPUT: "输出未通过校验",
  REJECTED: "请求被拒绝",
  CANCELLED: "已取消",
};
function money(value: number) {
  return (value / 1000000).toFixed(6);
}
function micros(value: string) {
  if (!/^\d{1,7}(\.\d{1,6})?$/.test(value))
    throw new Error("账单金额应为非负数，最多六位小数。");
  const [whole, fraction = ""] = value.split(".");
  const result = BigInt(whole!) * 1000000n + BigInt(fraction.padEnd(6, "0"));
  if (result > 1000000000000n) throw new Error("金额超出支持范围。");
  return Number(result);
}
async function perform(work: () => Promise<void>) {
  if (busy.value || disposed) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (!disposed) {
      error.value = message(e);
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        calls.value = [];
        current.value = null;
        history.value = { adjustments: [], receipts: [] };
        next.value = "";
        clear();
      }
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function load(more = false) {
  const data = await read<{ items: Call[]; next_cursor: string }>(
    `/model-calls${more ? `?after=${next.value}` : ""}`,
  );
  if (disposed) return;
  calls.value = more ? [...calls.value, ...data.items] : data.items;
  next.value = data.next_cursor;
}
function clear() {
  bill.value = "";
  evidence.value = "";
  reason.value = "";
  confirmed.value = false;
  amount.value = current.value ? money(current.value.charged_micros) : "";
}
async function select(call: Call) {
  if (dirty.value) return;
  await perform(async () => {
    const data = await read<History>(`/model-calls/${call.id}/history`);
    if (disposed) return;
    current.value = call;
    history.value = data;
    clear();
  });
}
async function record() {
  if (!current.value) return;
  const call = current.value;
  await perform(async () => {
    const value = micros(amount.value);
    const result = await api<Adjustment>(
      `/model-calls/${call.id}/reconcile`,
      "POST",
      {
        operation_id: operation.value,
        expected_revision: call.revision,
        currency: call.currency,
        amount_micros: value,
        bill_reference: bill.value,
        evidence: evidence.value,
        reason: reason.value,
        confirmed: confirmed.value,
      },
    );
    if (disposed) return;
    current.value = {
      ...call,
      revision: result.revision,
      charged_micros: value,
      state: call.state === "UNKNOWN" ? "RECONCILED" : call.state,
    };
    calls.value = calls.value.map((item) =>
      item.id === call.id ? current.value! : item,
    );
    clear();
    emit("recorded");
    const latest = await read<History>(`/model-calls/${call.id}/history`);
    if (disposed) return;
    history.value = latest;
    notice.value = "账单记录已保存，按差额更新本地记账；未发送任何模型请求。";
  });
}
function unload(event: BeforeUnloadEvent) {
  if (dirty.value || busy.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
onMounted(() => {
  window.addEventListener("beforeunload", unload);
  void perform(() => load());
});
onBeforeUnmount(() => {
  disposed = true;
  window.removeEventListener("beforeunload", unload);
  emit("blocking", false);
  emit("attention", { count: 0, loaded: 0, hasMore: false });
});
</script>
<template>
  <section class="panel reconciliation-panel" aria-label="请求费用与账单核对">
    <header class="section-heading">
      <div>
        <p class="eyebrow">请求账目</p>
        <h2>请求费用与账单核对</h2>
      </div>
      <button
        class="quiet"
        :disabled="busy || dirty"
        @click="perform(() => load())"
      >
        刷新请求账目
      </button>
    </header>
    <p class="author-page-description">
      按服务商账单或回执核对单次请求的累计费用。超时不等于免费；对账只更新本地记录，不退款、不重新生成内容。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <aside
      v-if="unknownCalls.length"
      class="billing-attention"
      aria-label="已读取的结果未知请求"
    >
      <div>
        <strong>{{ unknownCalls.length }} 项已读请求结果未知</strong>
        <p>费用占用仍保留，请凭账单或回执核对；不会自动重发请求。</p>
      </div>
      <button
        class="quiet"
        :disabled="busy || dirty"
        @click="
          stateFilter = 'UNKNOWN';
          query = '';
        "
      >
        定位结果未知请求
      </button>
    </aside>
    <div class="billing-layout" :class="{ 'has-current': current }">
      <section class="billing-list" aria-label="已读取请求列表">
        <div class="billing-filters">
          <label
            >搜索请求<input
              v-model="query"
              type="search"
              aria-label="搜索费用请求"
              :disabled="busy || dirty"
              placeholder="项目、模型或请求编号"
          /></label>
          <label
            >请求状态<select
              v-model="stateFilter"
              aria-label="筛选费用请求状态"
              :disabled="busy || dirty"
            >
              <option value="">全部状态</option>
              <option
                v-for="(name, state) in names"
                :key="state"
                :value="state"
              >
                {{ name }}
              </option>
            </select></label
          >
        </div>
        <p class="muted billing-list-count">
          已读取 {{ calls.length }} 条，当前显示 {{ filteredCalls.length }} 条{{
            next ? "；还有更早记录未读取" : ""
          }}。此处统计仅限已读取的请求。
        </p>
        <p v-if="!calls.length">还没有产生费用占用的请求。</p>
        <p v-else-if="!filteredCalls.length">
          没有符合当前筛选的已读请求，可调整搜索词或状态。
        </p>
        <ul class="billing-calls">
          <li v-for="call in filteredCalls" :key="call.id">
            <button
              class="quiet"
              :aria-pressed="current?.id === call.id"
              :disabled="busy || dirty"
              @click="select(call)"
            >
              {{ call.project_name }} · {{ call.model || "请求" }} ·
              {{ names[call.state] ?? call.state }} ·
              {{ money(call.charged_micros) }} {{ call.currency }}
            </button>
          </li>
        </ul>
        <button
          v-if="next"
          class="quiet"
          :disabled="busy || dirty"
          @click="perform(() => load(true))"
        >
          加载更早请求
        </button>
      </section>
      <section v-if="current" class="billing-detail">
        <h3>本次核对 · {{ current.project_name }}</h3>
        <p>
          请求 {{ current.id }} · {{ current.provider }} {{ current.model }}
        </p>
        <p>
          当前记账 {{ money(current.charged_micros) }} {{ current.currency }} ·
          {{ names[current.state] ?? current.state }}
        </p>
        <p v-if="current.state === 'UNKNOWN'" class="billing-current-unknown">
          结果未知占用 {{ money(current.reserved_micros) }}
          {{ current.currency }}。请先核对最终累计金额，超时不能按零收费判断。
        </p>
        <p v-if="current.provider_receipt">
          服务商回执：{{ current.provider_receipt }}
        </p>
        <form v-if="current.can_reconcile" @submit.prevent="record">
          <fieldset :disabled="busy">
            <label
              >此请求的账单累计金额（{{ current.currency }}）<input
                v-model="amount"
                inputmode="decimal"
                required
                aria-label="账单累计金额"
            /></label>
            <label
              >账单或回执编号<input v-model="bill" required maxlength="200"
            /></label>
            <label
              >核对依据<textarea
                v-model="evidence"
                rows="3"
                required
                minlength="10"
                maxlength="2500"
                placeholder="记录账户、日期、请求匹配依据和账单摘录，不填写 API Key。"
              />
            </label>
            <label
              >本次调整原因<input v-model="reason" required maxlength="300"
            /></label>
            <p>
              填写的是此请求的最终累计金额。再次修正时只计入差额，历史对账记录继续保留。
            </p>
            <label class="confirmation"
              ><input
                v-model="confirmed"
                type="checkbox"
                required
              />我已核对账单与此请求的对应关系</label
            >
            <button :disabled="!confirmed">确认保存账单记录</button>
            <button class="quiet" type="button" @click="clear">
              放弃本次账单编辑
            </button>
          </fieldset>
        </form>
        <p v-else>
          此请求尚不适合对账。已发送的请求需先取得结果或明确进入未知状态。
        </p>
        <details class="adjustment-history">
          <summary>历次对账 · {{ history.adjustments.length }} 条</summary>
          <p v-if="!history.adjustments.length">尚无人工对账记录。</p>
          <article
            v-for="item in history.adjustments"
            :key="item.id"
            class="adjustment"
          >
            <p>
              第 {{ item.revision }} 版 ·
              {{
                item.retention_state === "EXPIRED"
                  ? "已保留费用证明"
                  : item.input.bill_reference
              }}
              · {{ money(item.previous_amount_micros) }} →
              {{ money(item.input.amount_micros) }} {{ item.input.currency }}
            </p>
            <p v-if="item.retention_state === 'EXPIRED'">
              原始账单说明已到保留期限，金额、版本和费用处理记录仍然保留。
            </p>
            <template v-else>
              <p>{{ item.input.reason }}</p>
              <blockquote>{{ item.input.evidence }}</blockquote>
            </template>
          </article>
        </details>
        <details class="receipt-history">
          <summary>
            自动回执与迟到回执 · {{ history.receipts.length }} 条
          </summary>
          <p>
            自动回执与人工账单不同会继续保留，需按证据再次核对，不自动覆盖已确认金额。
          </p>
          <ul>
            <li v-for="(receipt, index) in history.receipts" :key="index">
              <template v-if="Array.isArray(receipt)">
                {{ names[receipt[0]] ?? receipt[0] }} ·
                {{
                  receipt[0] === "UNKNOWN"
                    ? "没有确切用量，不能按零收费判断"
                    : money(receipt[1]) + " " + current.currency
                }}
                · {{ receipt[2] || "无服务商编号" }}
              </template>
              <template v-else>
                {{ names[receipt.outcome] ?? receipt.outcome }} ·
                {{
                  receipt.outcome === "UNKNOWN"
                    ? "没有确切用量，不能按零收费判断"
                    : money(receipt.amount_micros) + " " + current.currency
                }}
                · 原始回执已到保留期限，费用证明仍保留
              </template>
            </li>
          </ul>
        </details>
      </section>
    </div>
  </section>
</template>
<style scoped>
.billing-attention {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin: 20px 0;
  padding: 16px;
  border: 1px solid #d8d1b7;
  border-radius: 10px;
  background: #f6f2e7;
}
.billing-attention p {
  margin: 4px 0 0;
  font-size: 13px;
}
.billing-attention button {
  flex-shrink: 0;
}
.billing-layout.has-current {
  display: grid;
  grid-template-columns: minmax(250px, 0.8fr) minmax(0, 1.4fr);
  gap: 28px;
  align-items: start;
}
.billing-list,
.billing-detail {
  min-width: 0;
}
.billing-filters {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.has-current .billing-filters {
  grid-template-columns: minmax(0, 1fr);
}
.billing-list-count {
  font-size: 12px;
}
.billing-calls {
  display: grid;
  gap: 10px;
  padding: 0;
  list-style: none;
  margin: 16px 0;
}
.billing-calls button {
  width: 100%;
  text-align: left;
  line-height: 1.7;
  font-size: 13px;
  overflow-wrap: anywhere;
}
.billing-calls button[aria-pressed="true"] {
  background: #edf1e6;
  border-color: #91ad98;
  color: #285c4d;
}
.billing-detail {
  overflow-wrap: anywhere;
  border-left: 1px solid #e1e5da;
  padding-left: 28px;
}
.billing-detail h3 {
  margin-top: 0;
}
.billing-current-unknown {
  padding: 12px;
  border-left: 3px solid #b5a26b;
  background: #f6f2e7;
  font-size: 13px;
}
.billing-detail details {
  margin-top: 20px;
  padding-top: 18px;
  border-top: 1px solid #e1e5da;
}
.billing-detail summary {
  cursor: pointer;
  font-weight: 600;
  color: #52665d;
}
.adjustment {
  padding: 1rem 0;
  border-top: 1px solid var(--line);
}
blockquote {
  white-space: pre-wrap;
  margin: 0;
  padding: 1rem;
  background: var(--paper);
}
fieldset {
  display: grid;
  gap: 1rem;
  border: 0;
  padding: 0;
}
.confirmation {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.confirmation input {
  width: auto;
  margin: 0;
}
@media (max-width: 780px) {
  .billing-layout.has-current {
    grid-template-columns: minmax(0, 1fr);
  }
  .billing-detail {
    border-left: 0;
    border-top: 1px solid #e1e5da;
    padding: 24px 0 0;
  }
  .billing-attention {
    flex-direction: column;
    align-items: flex-start;
  }
}
@media (max-width: 520px) {
  .billing-filters {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
