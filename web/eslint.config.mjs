import vue from "eslint-plugin-vue";
import ts from "typescript-eslint";

export default ts.config(
  {
    ignores: [
      "dist/**",
      "node_modules/**",
      "test-results/**",
      "playwright-report/**",
    ],
  },
  ...ts.configs.recommended,
  ...vue.configs["flat/essential"],
  {
    files: ["**/*.vue"],
    languageOptions: {
      parserOptions: { parser: ts.parser, extraFileExtensions: [".vue"] },
    },
    rules: { "vue/no-mutating-props": ["error", { shallowOnly: true }] },
  },
);
