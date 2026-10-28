export interface Actor {
  id: string;
  workspace_id: string;
  username: string;
  role: string;
}
export interface Project {
  id: string;
  name: string;
  revision: number;
  role: string;
}

export class APIError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly fields: Record<string, string> = {},
  ) {
    super(message);
  }
}

export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  options?: { signal?: AbortSignal },
): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`/api${path}`, {
      method,
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: options?.signal,
    });
  } catch {
    throw new APIError("暂时无法连接工作室。你的输入仍然保留，可以重试。", 0);
  }
  const data = await response.json().catch((error: unknown) => {
    if (!response.ok)
      throw new APIError(
        "请求未获成功响应，请核对权限或状态。",
        response.status,
      );
    throw error;
  });
  if (!response.ok)
    throw new APIError(
      data?.error ?? data?.message ?? "操作未完成，请重试",
      response.status,
      data?.fields && typeof data.fields === "object"
        ? (Object.fromEntries(
            Object.entries(data.fields).filter(
              ([, value]) => typeof value === "string",
            ),
          ) as Record<string, string>)
        : {},
    );
  return data as T;
}

export function message(error: unknown): string {
  return error instanceof Error ? error.message : "操作未完成，请重试";
}

// New requests use an opaque namespace; existing saved replay keys stay unchanged.
export function newOperationID(): string {
  return `request-v2:${crypto.randomUUID().replaceAll("-", "")}`;
}
