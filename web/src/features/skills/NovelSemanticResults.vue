<script setup lang="ts">
import type { NovelSemanticSummary } from "./novelSemanticTypes";
defineProps<{ summary: NovelSemanticSummary }>();
const arms: Record<string, string> = {
  no_method: "不使用方法",
  baseline: "现有基线",
  candidate: "候选版本",
};
const names: Record<string, string> = {
  identity: "身份与人物连续性",
  chronology: "时间与顺序",
  knowledge: "知情范围与视角",
  epistemic: "事实、信念与传闻",
  world_limits: "世界限制与例外",
  resources: "有限资源与状态",
  causality: "因果与前置条件",
  relations: "关系与阵营",
  story_plan: "主线、伏笔与高光",
  revision_scope: "修订与分支边界",
  world_rule: "世界规则",
  fact_belief_claim: "事实与认知混淆",
  temporal: "时间矛盾",
  knowledge_visibility: "越界知情",
  causal: "因果问题",
  outline_chapter_consistency: "章节与规划不一致",
};
function percent(value: number, defined = true) {
  return defined && Number.isFinite(value)
    ? `${(value * 100).toFixed(1)}%`
    : "尚无可计算结果";
}
</script>

<template>
  <section class="novel-semantic-results" aria-label="小说语义验收统计">
    <h3>小说语义验收统计</h3>
    <p>{{ summary.boundary }}</p>
    <p>
      当前是工程样例标注，独立人工复核尚未完成。误报是把允许情形当作问题，漏报是没有识别实际缺陷；严格通过还要求问题类别与约束、正文双证据一致。
    </p>
    <p>
      候选需完整运行，费用和使用量已对账，且精确率、召回率、严格通过率、有证据召回率均至少达到
      {{
        percent(summary.threshold)
      }}。没有检出任何问题时，精确率显示为尚无结果。
    </p>
    <table>
      <thead>
        <tr>
          <th>对照组</th>
          <th>完成</th>
          <th>精确率</th>
          <th>召回率</th>
          <th>严格通过</th>
          <th>有证据召回</th>
          <th>误报 / 漏报</th>
          <th>结果</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="arm in summary.arms" :key="arm.arm">
          <td>{{ arms[arm.arm] || arm.arm }}</td>
          <td>{{ arm.observed }} / {{ arm.cases }}</td>
          <td>{{ percent(arm.precision, arm.precision_defined) }}</td>
          <td>{{ percent(arm.recall, arm.recall_defined) }}</td>
          <td>{{ percent(arm.strict_accuracy) }}</td>
          <td>{{ percent(arm.supported_recall) }}</td>
          <td>{{ arm.false_positive }} / {{ arm.false_negative }}</td>
          <td>
            {{
              arm.passed
                ? "本样例集通过"
                : arm.pending
                  ? `还有 ${arm.pending} 项待运行`
                  : "未通过"
            }}
          </td>
        </tr>
      </tbody>
    </table>
    <details v-for="arm in summary.arms" :key="`${arm.arm}-details`">
      <summary>{{ arms[arm.arm] || arm.arm }}：分类表现与未完成项</summary>
      <p>
        缺陷 {{ arm.defects }} 项，允许情形 {{ arm.allowed }} 项；输出无效
        {{ arm.invalid }} 项，回答无法确定 {{ arm.abstained }} 项，待对账
        {{ arm.unsettled }} 项；重复记录 {{ arm.duplicate_steps }} 项，未知样例
        {{ arm.unknown_steps }} 项。
      </p>
      <table>
        <thead>
          <tr>
            <th>分类口径</th>
            <th>类别</th>
            <th>完成</th>
            <th>精确率</th>
            <th>召回率</th>
            <th>严格通过</th>
            <th>误报 / 漏报</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in arm.breakdown" :key="`${row.kind}/${row.name}`">
            <td>{{ row.kind === "topic" ? "场景主题" : "问题类别" }}</td>
            <td>{{ names[row.name] || row.name }}</td>
            <td>{{ row.observed }} / {{ row.cases }}</td>
            <td>{{ percent(row.precision, row.precision_defined) }}</td>
            <td>{{ percent(row.recall, row.recall_defined) }}</td>
            <td>{{ percent(row.strict_accuracy) }}</td>
            <td>{{ row.false_positive }} / {{ row.false_negative }}</td>
          </tr>
        </tbody>
      </table>
    </details>
  </section>
</template>

<style scoped>
.novel-semantic-results {
  margin: 20px 0;
  overflow-x: auto;
}
table {
  width: 100%;
  min-width: 760px;
  margin: 12px 0;
  border-collapse: collapse;
  font-variant-numeric: tabular-nums;
}
th,
td {
  padding: 9px 10px;
  border-bottom: 1px solid #dce1d8;
  text-align: left;
  vertical-align: top;
  line-height: 1.5;
}
th {
  background: #f2f4ee;
  white-space: nowrap;
}
details {
  margin: 10px 0;
}
summary {
  padding: 8px 0;
  cursor: pointer;
}
</style>
