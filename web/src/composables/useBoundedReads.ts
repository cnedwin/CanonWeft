import { onBeforeUnmount } from "vue";
import { api, APIError } from "../api";

// Only GET requests use this lifecycle. Never cancel/retry a mutation or infer
// an unknown write outcome from the read deadline.
export function useBoundedReads(messages: {
  stopped: string;
  timeout: string;
}) {
  let disposed = false;
  const pending = new Set<AbortController>();
  onBeforeUnmount(() => {
    disposed = true;
    for (const controller of pending) controller.abort();
  });
  async function read<T>(path: string): Promise<T> {
    if (disposed) throw new APIError(messages.stopped, 0);
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
      // Preserve a known denial even when its response body hangs or is invalid.
      if (error instanceof APIError && error.status !== 0) throw error;
      if (timedOut) throw new APIError(messages.timeout, 0);
      throw error;
    } finally {
      clearTimeout(timer);
      pending.delete(controller);
    }
  }
  return { read, active: () => !disposed };
}
