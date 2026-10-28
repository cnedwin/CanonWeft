import json
import os
import httpx


class UnknownOutcome(Exception):
    """Transport failed after a request may have reached the provider."""


class Rejected(Exception):
    """Provider definitively rejected a request without generation."""


class OpenAIProvider:
    model = 'gpt-6.1-sol'

    def __init__(self, key=None, transport=None):
        self._key = key or os.environ.get('OPENAI_API_KEY', '')
        self.transport = transport

    @property
    def ready(self):
        return bool(self._key)

    def set_key(self, key):
        self._key = key.strip()

    def generate(self, role, instructions, payload, schema, max_output_tokens):
        if not self.ready:
            raise Rejected('尚未设置 API 密钥')
        request = {'model': self.model, 'instructions': instructions, 'input': json.dumps(payload, ensure_ascii=False), 'store': False,
                   'reasoning': {'effort': 'low'}, 'max_output_tokens': max_output_tokens,
                   'text': {'format': {'type': 'json_schema', 'name': role + '_output', 'strict': True, 'schema': schema}}}
        try:
            with httpx.Client(timeout=httpx.Timeout(240, connect=20), transport=self.transport, trust_env=False) as client:
                result = client.post('https://api.openai.com/v1/responses', headers={'Authorization': 'Bearer ' + self._key}, json=request)
        except httpx.HTTPError as exc:
            raise UnknownOutcome('网络中断或超时，服务端是否计费尚不确定；已保留预算，不会自动重试。') from exc
        if 400 <= result.status_code < 500:
            raise Rejected(f'模型服务拒绝请求（HTTP {result.status_code}）。请检查密钥、模型权限和账户额度。')
        if result.status_code != 200:
            raise UnknownOutcome(f'模型服务返回 HTTP {result.status_code}，结果未知，未自动重试。')
        try:
            raw = result.json()
            usage = raw['usage']
            text = ''.join(c.get('text', '') for o in raw.get('output', []) if o.get('type') == 'message' for c in o.get('content', []) if c.get('type') == 'output_text')
            try:
                data = json.loads(text)
            except (ValueError, TypeError):
                data = {'_invalid_output': '模型未返回完整结构化结果；可能触发长度限制或拒绝。'}
            return {'data': data, 'response_id': raw.get('id'), 'input_tokens': usage['input_tokens'], 'output_tokens': usage['output_tokens'], 'status': raw.get('status'), 'raw_text': text}
        except (ValueError, KeyError, TypeError) as exc:
            raise UnknownOutcome('响应缺少可核实的用量记录，暂按预留上限占用预算。') from exc
