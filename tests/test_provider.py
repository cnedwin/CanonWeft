import httpx
import pytest
from studio.provider import OpenAIProvider, Rejected, UnknownOutcome
from studio.models import Draft


def test_responses_adapter_sends_schema_and_accounts_reasoning_tokens():
    def handler(request):
        import json
        data = json.loads(request.content)
        assert request.url == 'https://api.openai.com/v1/responses'
        assert data['store'] is False
        assert data['text']['format']['strict'] is True
        assert data['text']['format']['schema']['additionalProperties'] is False
        assert data['max_output_tokens'] == 10000
        assert 'previous_response_id' not in data
        return httpx.Response(200, json={'id':'resp_unit','status':'completed','output':[{'type':'reasoning','summary':[]},{'type':'message','role':'assistant','content':[{'type':'output_text','text':'{"title":"雨城","body":"林舟停在门前。"}','annotations':[]}]}], 'usage':{'input_tokens':100,'output_tokens':150,'output_tokens_details':{'reasoning_tokens':50}}})
    p = OpenAIProvider('test-only', httpx.MockTransport(handler))
    result = p.generate('writer', '写正文', {'brief':'雨城'}, Draft.model_json_schema(), 10000)
    assert result['data']['body'] == '林舟停在门前。'
    assert result['output_tokens'] == 150


@pytest.mark.parametrize('status,exception',[(401,Rejected),(429,Rejected),(500,UnknownOutcome)])
def test_no_automatic_network_retry(status, exception):
    count = 0
    def handler(request):
        nonlocal count
        count += 1
        return httpx.Response(status,json={'error':{'message':'test'}})
    p = OpenAIProvider('test',httpx.MockTransport(handler))
    with pytest.raises(exception):
        p.generate('writer','write',{},Draft.model_json_schema(),10000)
    assert count == 1
