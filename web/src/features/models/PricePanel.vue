<script setup lang="ts">
import { computed, ref } from "vue";
import { api, APIError, message } from "../../api";
import { useModelReads } from "./modelReads";
const props = defineProps<{
  connections: { id: string; name: string; model: string; revision: number }[];
}>();
const emit = defineEmits<{ expired: [] }>();
const connection = ref("");
const currency = ref("USD");
const input = ref("");
const output = ref("");
const read = ref("");
const write = ref("");
const source = ref("");
const days = ref(7);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const image = ref(false);
const imageInput = ref("");
const textOutput = ref("0");
interface PriceCard {
  id: string;
  connection_id: string;
  connection_revision: number;
  currency: string;
  input_micros_per_million: number;
  output_micros_per_million: number;
  cache_read_micros_per_million: number;
  cache_write_micros_per_million: number;
  source: string;
  expires_at: string;
  image?: {
    text_input_micros_per_million: number;
    image_input_micros_per_million: number;
    text_output_micros_per_million: number;
    image_output_micros_per_million: number;
  };
}
const priceReads = useModelReads();
const prices = ref<PriceCard[]>([]),
  pricesBusy = ref(false),
  pricesLoaded = ref(false),
  pricesStale = ref(false),
  pricesError = ref("");
const priceGroups = computed(() => {
  const groups = new Map<
    string,
    { id: string; name: string; items: PriceCard[] }
  >();
  for (const price of prices.value) {
    let group = groups.get(price.connection_id);
    if (!group) {
      group = {
        id: price.connection_id,
        name:
          props.connections.find((item) => item.id === price.connection_id)
            ?.name ?? `已存配置 ${price.connection_id.slice(0, 8)}`,
        items: [],
      };
      groups.set(price.connection_id, group);
    }
    group.items.push(price);
  }
  return [...groups.values()];
});
function object(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function validPrice(value: unknown): value is PriceCard {
  if (
    !object(value) ||
    !["id", "connection_id", "currency", "source", "expires_at"].every(
      (key) => typeof value[key] === "string",
    ) ||
    !value.id
  )
    return false;
  if (
    ![
      "connection_revision",
      "input_micros_per_million",
      "output_micros_per_million",
      "cache_read_micros_per_million",
      "cache_write_micros_per_million",
    ].every(
      (key) => Number.isSafeInteger(value[key]) && Number(value[key]) >= 0,
    )
  )
    return false;
  if (value.image !== undefined) {
    const rates = value.image;
    if (
      !object(rates) ||
      ![
        "text_input_micros_per_million",
        "image_input_micros_per_million",
        "text_output_micros_per_million",
        "image_output_micros_per_million",
      ].every(
        (key) => Number.isSafeInteger(rates[key]) && Number(rates[key]) >= 0,
      )
    )
      return false;
  }
  return true;
}
function priceAmount(value: number) {
  return (value / 1000000).toLocaleString(undefined, {
    maximumFractionDigits: 6,
  });
}
function priceTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "时间无法识别" : date.toLocaleString();
}
async function loadPrices() {
  if (pricesBusy.value || busy.value || !priceReads.active()) return;
  pricesBusy.value = true;
  pricesError.value = "";
  try {
    const value = await priceReads.read<unknown>("/model-prices");
    if (!priceReads.active()) return;
    if (
      !object(value) ||
      !Array.isArray(value.items) ||
      !value.items.every(validPrice)
    )
      throw new Error("价格列表数据不完整，请重新读取；编辑内容仍保留。");
    prices.value = value.items;
    pricesLoaded.value = true;
    pricesStale.value = false;
  } catch (e) {
    if (!priceReads.active()) return;
    pricesError.value = message(e);
    if (e instanceof APIError && [401, 403].includes(e.status)) {
      prices.value = [];
      pricesLoaded.value = false;
      pricesStale.value = false;
    }
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    if (priceReads.active()) pricesBusy.value = false;
  }
}
function micros(value: string) {
  if (!/^\d{1,4}(\.\d{1,6})?$/.test(value))
    throw new Error("请填写非负价格，最多六位小数。");
  const [whole, fraction = ""] = value.split(".");
  return Number(
    BigInt(whole ?? "0") * 1000000n + BigInt(fraction.padEnd(6, "0")),
  );
}
async function save() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    const config = props.connections.find((c) => c.id === connection.value);
    if (!config) throw new Error("先选择模型配置。");
    await api("/model-prices", "POST", {
      connection_id: config.id,
      connection_revision: config.revision,
      currency: currency.value,
      input_micros_per_million: image.value ? 0 : micros(input.value),
      output_micros_per_million: image.value ? 0 : micros(output.value),
      cache_read_micros_per_million: image.value ? 0 : micros(read.value),
      cache_write_micros_per_million: image.value ? 0 : micros(write.value),
      ...(image.value
        ? {
            image: {
              text_input_micros_per_million: micros(input.value),
              image_input_micros_per_million: micros(imageInput.value),
              text_output_micros_per_million: micros(textOutput.value),
              image_output_micros_per_million: micros(output.value),
            },
          }
        : {}),
      source: source.value,
      expires_at: new Date(Date.now() + days.value * 86400000).toISOString(),
    });
    notice.value = "价格版本已保存，未发起任何模型调用。";
    if (pricesLoaded.value) pricesStale.value = true;
  } catch (e) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <section class="panel price-panel" aria-label="模型价格与费用估算">
    <header class="section-heading">
      <div>
        <p class="eyebrow">价格版本</p>
        <h2>模型价格与费用估算</h2>
      </div>
    </header>
    <p class="author-page-description">
      按你的服务区域、账户和上下文档位填写官方价格，单位为每百万
      token。价格到期后阻止新请求；任务固定使用准备时的价格版本。用量记账仍需与供应商账单核对。
    </p>
    <form @submit.prevent="save">
      <fieldset :disabled="busy">
        <legend>保存新的价格版本</legend>
        <div class="price-config-grid">
          <label
            >价格对应配置<select v-model="connection" required>
              <option value="" disabled>选择模型配置</option>
              <option v-for="c in connections" :key="c.id" :value="c.id">
                {{ c.name }} · {{ c.model }} · 版本 {{ c.revision }}
              </option>
            </select></label
          >
          <label
            >价格币种<select v-model="currency">
              <option value="USD">美元 USD</option>
              <option value="CNY">人民币 CNY</option>
            </select></label
          >
        </div>
        <label
          >计费类型<select v-model="image">
            <option :value="false">文字模型</option>
            <option :value="true">图片模型（文字 / 图片分别计费）</option>
          </select></label
        >
        <div v-if="image" class="price-grid">
          <label
            >文字输入价格 / 百万 token<input
              v-model="input"
              inputmode="decimal"
              required
          /></label>
          <label
            >图片输入价格 / 百万 token<input
              v-model="imageInput"
              inputmode="decimal"
              required
          /></label>
          <label
            >文字输出价格 / 百万 token<input
              v-model="textOutput"
              inputmode="decimal"
              required
          /></label>
          <label
            >图片输出价格 / 百万 token<input
              v-model="output"
              inputmode="decimal"
              required
          /></label>
        </div>
        <div v-else class="price-grid">
          <label
            >输入价格 / 百万 token<input
              v-model="input"
              inputmode="decimal"
              required /></label
          ><label
            >输出价格 / 百万 token<input
              v-model="output"
              inputmode="decimal"
              required /></label
          ><label
            >缓存读取价格 / 百万 token<input
              v-model="read"
              inputmode="decimal"
              required /></label
          ><label
            >缓存写入价格 / 百万 token<input
              v-model="write"
              inputmode="decimal"
              required
          /></label>
        </div>
        <div class="price-source-grid">
          <label
            >官方价格来源<input
              v-model="source"
              type="url"
              required
              placeholder="https://..." /></label
          ><label
            >价格有效天数<input
              v-model.number="days"
              type="number"
              min="1"
              max="30"
              required
          /></label>
        </div>
        <div><button>保存价格版本</button></div>
      </fieldset>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <details class="saved-prices">
      <summary>
        查看已存价格版本{{
          pricesLoaded ? ` · 已读取 ${prices.length} 个` : ""
        }}
      </summary>
      <p class="muted">
        按需读取已保存的价格，供核对来源、计费类型和有效期；查看列表不会改变用途绑定或正在编辑的内容。列表为读取时的快照，保存新版本后请刷新核对；任务仍使用准备时固定的价格版本。
      </p>
      <button
        type="button"
        class="quiet"
        :disabled="pricesBusy || busy"
        @click="loadPrices"
      >
        {{
          pricesBusy
            ? "正在读取价格…"
            : pricesLoaded
              ? "刷新已存价格版本"
              : "读取已存价格版本"
        }}
      </button>
      <p v-if="pricesError" role="alert" class="error">{{ pricesError }}</p>
      <p v-if="pricesStale" class="muted">
        刚保存了新版本；以下仍是上次读取的价格快照，请刷新列表核对。
      </p>
      <p v-if="pricesLoaded && !prices.length">尚无已保存的价格版本。</p>
      <section
        v-for="group in priceGroups"
        :key="group.id"
        class="saved-price-group"
      >
        <h3>
          {{ group.name }} <small>{{ group.items.length }} 个已读取版本</small>
        </h3>
        <article
          v-for="price in group.items"
          :key="price.id"
          class="saved-price"
        >
          <div class="saved-price-heading">
            <strong
              >{{ price.image ? "图片计费" : "文字计费" }} ·
              {{ price.currency }}</strong
            ><span
              >配置版本 {{ price.connection_revision }} · 价格
              {{ price.id.slice(0, 8) }}</span
            >
          </div>
          <p class="muted">
            有效至 {{ priceTime(price.expires_at) }} · 以下均为每百万 token
            的价格
          </p>
          <dl v-if="price.image" class="saved-price-rates">
            <div>
              <dt>文字输入</dt>
              <dd>
                {{ priceAmount(price.image.text_input_micros_per_million) }}
              </dd>
            </div>
            <div>
              <dt>图片输入</dt>
              <dd>
                {{ priceAmount(price.image.image_input_micros_per_million) }}
              </dd>
            </div>
            <div>
              <dt>文字输出</dt>
              <dd>
                {{ priceAmount(price.image.text_output_micros_per_million) }}
              </dd>
            </div>
            <div>
              <dt>图片输出</dt>
              <dd>
                {{ priceAmount(price.image.image_output_micros_per_million) }}
              </dd>
            </div>
          </dl>
          <dl v-else class="saved-price-rates">
            <div>
              <dt>输入</dt>
              <dd>{{ priceAmount(price.input_micros_per_million) }}</dd>
            </div>
            <div>
              <dt>输出</dt>
              <dd>{{ priceAmount(price.output_micros_per_million) }}</dd>
            </div>
            <div>
              <dt>缓存读取</dt>
              <dd>{{ priceAmount(price.cache_read_micros_per_million) }}</dd>
            </div>
            <div>
              <dt>缓存写入</dt>
              <dd>{{ priceAmount(price.cache_write_micros_per_million) }}</dd>
            </div>
          </dl>
          <p class="saved-price-source">记录的价格来源：{{ price.source }}</p>
        </article>
      </section>
    </details>
  </section>
</template>
<style scoped>
fieldset {
  display: grid;
  gap: 20px;
  min-width: 0;
  border: 0;
  padding: 0;
  margin: 0;
}
legend {
  padding: 0;
  margin-bottom: 20px;
  font-weight: 600;
}
.price-config-grid,
.price-source-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 18px;
}
.price-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 1rem;
}
.saved-prices {
  border-top: 1px solid #e1e5da;
  margin-top: 28px;
  padding-top: 20px;
}
.saved-prices summary {
  cursor: pointer;
  font-weight: 600;
  color: #52665d;
}
.saved-price-group {
  margin-top: 24px;
}
.saved-price-group h3 small {
  margin-left: 10px;
  font-size: 12px;
  font-weight: 400;
  color: #5d6e63;
}
.saved-price {
  margin-top: 12px;
  padding: 18px;
  border: 1px solid #e1e5da;
  border-radius: 10px;
  background: #f5f6ef;
}
.saved-price-heading {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 8px;
}
.saved-price-heading span,
.saved-price p,
.saved-price-rates dt {
  font-size: 12px;
  color: #5d6e63;
}
.saved-price-rates {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin: 16px 0;
}
.saved-price-rates dd {
  margin: 6px 0 0;
  font-variant-numeric: tabular-nums;
}
.saved-price-source {
  margin-bottom: 0;
  overflow-wrap: anywhere;
}
@media (max-width: 680px) {
  .price-config-grid,
  .price-source-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .saved-price-rates {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
