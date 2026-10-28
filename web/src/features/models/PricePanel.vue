<script setup lang="ts">
import { ref } from "vue";
import { api, APIError, message } from "../../api";
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
  } catch (e) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <section class="panel">
    <h2>模型价格与费用估算</h2>
    <p class="muted">
      按你的服务区域、账户和上下文档位填写官方价格，单位为每百万
      token。价格到期后阻止新请求；任务固定使用准备时的价格版本。用量记账仍需与供应商账单核对。
    </p>
    <form @submit.prevent="save">
      <fieldset :disabled="busy">
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
            required /></label
        ><button>保存价格版本</button>
      </fieldset>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
  </section>
</template>
<style scoped>
.price-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 1rem;
}
label {
  margin-bottom: 1rem;
}
</style>
