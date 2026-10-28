import { onBeforeUnmount } from "vue";
import { api, APIError } from "../../api";

// Settings reads must not hold the editor indefinitely. This helper cannot
// dispatch or cancel mutations, infer their outcome, or release fee reservations.
export function useModelReads() {
  let disposed = false;
  const pending = new Set<AbortController>();
  onBeforeUnmount(() => {
    disposed = true;
    for (const controller of pending) controller.abort();
  });
  async function read<T>(path: string): Promise<T> {
    if (disposed) throw new APIError("设置读取已停止。", 0);
    const controller = new AbortController();
    pending.add(controller);
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, 15000);
    try {
      return await api<T>(path, "GET", undefined, {
        signal: controller.signal,
      });
    } catch (error) {
      // A known denial remains a denial even when its response body hangs.
      if (error instanceof APIError && error.status !== 0) throw error;
      if (timedOut)
        throw new APIError("设置读取超时，未保存输入仍保留。请稍后刷新。", 0);
      throw error;
    } finally {
      clearTimeout(timer);
      pending.delete(controller);
    }
  }
  return { read, active: () => !disposed };
}
