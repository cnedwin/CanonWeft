let token = '';
export async function api<T = any>(path: string, options: RequestInit = {}): Promise<T> {
  const isForm = options.body instanceof FormData;
  const response = await fetch('/api' + path, {...options, headers: {'X-Studio-Token': token, ...(!isForm && options.body ? {'Content-Type': 'application/json'} : {}), ...options.headers}});
  if (!response.ok) {
    let message = '请求失败，请稍后重试';
    try { const d = await response.json(); message = typeof d.detail === 'string' ? d.detail : '输入格式不正确，请检查各项内容'; } catch {}
    throw new Error(message);
  }
  return response.json();
}
export async function initialize() { const session = await api('/session'); token = session.token; }
export function post(path: string, value: unknown = {}) { return api(path, {method:'POST', body: JSON.stringify(value)}); }
export function put(path: string, value: unknown) { return api(path, {method:'PUT', body: JSON.stringify(value)}); }
export async function download(path: string, filename: string) {
  const res = await fetch('/api' + path, {headers:{'X-Studio-Token': token}});
  if (!res.ok) { const d = await res.json(); throw new Error(d.detail || '导出失败'); }
  const url = URL.createObjectURL(await res.blob());
  const link = document.createElement('a'); link.href = url; link.download = filename; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
