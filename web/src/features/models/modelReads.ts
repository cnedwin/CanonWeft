import { useBoundedReads } from "../../composables/useBoundedReads";

// Settings reads must not hold the editor indefinitely. This helper cannot
// dispatch or cancel mutations, infer their outcome, or release fee reservations.
export function useModelReads() {
  return useBoundedReads({
    stopped: "设置读取已停止。",
    timeout: "设置读取超时，未保存输入仍保留。请稍后刷新。",
  });
}
