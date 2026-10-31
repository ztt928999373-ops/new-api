"""Black-box protocol/ledger fixtures. Never prints credentials or raw API bodies."""
import argparse
import hashlib
import http.cookiejar
import json
import os
import time
import urllib.error
import urllib.request

BASE = 'http://mock:3000'
OPENER = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
AUTH = None
KEY = None
ROWS = []


def request(path, data=None, method=None, headers=None, management=False):
    h = {'Content-Type': 'application/json', **(headers or {})}
    if management and AUTH:
        h['Authorization'] = 'Bearer ' + AUTH
    req = urllib.request.Request(BASE + path, None if data is None else json.dumps(data).encode(), h, method=method)
    try:
        with OPENER.open(req, timeout=15) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def api(path, data=None, method=None):
    status, body = request(path, data, method, management=True)
    value = json.loads(body)
    assert status == 200 and value.get('success'), f'management failure {path} HTTP {status}'
    return value.get('data')


def items(data):
    return data if isinstance(data, list) else data['items']


def snapshot():
    user = api('/api/user/self')
    tokens = items(api('/api/token/?p=1&page_size=100'))
    token = next(row for row in tokens if row['name'] == 'phase2-fixture')
    logs = items(api('/api/log/?p=1&page_size=100'))
    consume = [row for row in logs if row['type'] == 2]
    channels = items(api('/api/channel/?p=1&page_size=100'))
    total = sum(row['quota'] for row in consume)
    assert user['quota'] == 100000000 - total
    assert token['remain_quota'] == 1000000 - total and token['used_quota'] == total
    assert len(channels) == 7 and len(tokens) == 1
    summary = {'quota': user['quota'], 'token_remaining': token['remain_quota'],
               'used_quota': total, 'consume_logs': len(consume), 'channels': len(channels)}
    return summary


def check(name, fn):
    try:
        detail = fn()
        row = {'case': name, 'status': 'PASS', **(detail or {})}
    except Exception as e:
        row = {'case': name, 'status': 'FAIL', 'reason': str(e) if isinstance(e, AssertionError) else type(e).__name__}
    ROWS.append(row)
    print(json.dumps(row), flush=True)


def protocol_case(protocol, stream=False, tool=False, model=None, status_expected=200):
    model = model or ('mock-tool' if tool else 'mock-text')
    schema = {'type': 'object', 'properties': {'city': {'type': 'string'}}, 'required': ['city']}
    before = snapshot()
    if model == 'mock-fallback':
        req = urllib.request.Request('http://mock:8000/reset', b'{}', {'Content-Type': 'application/json'})
        urllib.request.urlopen(req, timeout=2).close()
    if protocol == 'chat':
        path = '/v1/chat/completions'
        payload = {'model': model, 'messages': [{'role': 'user', 'content': 'synthetic fixture'}],
                   'max_tokens': 10, 'stream': stream}
        if stream:
            payload['stream_options'] = {'include_usage': True}
        if tool:
            payload['tools'] = [{'type': 'function', 'function': {'name': 'get_weather', 'parameters': schema}}]
        headers = {'Authorization': 'Bearer ' + KEY}
    elif protocol == 'responses':
        path = '/v1/responses'
        payload = {'model': model, 'input': 'synthetic fixture', 'max_output_tokens': 10, 'stream': stream}
        if tool:
            payload['tools'] = [{'type': 'function', 'name': 'get_weather', 'parameters': schema}]
        headers = {'Authorization': 'Bearer ' + KEY}
    elif protocol == 'messages':
        path = '/v1/messages'
        payload = {'model': model, 'messages': [{'role': 'user', 'content': 'synthetic fixture'}],
                   'max_tokens': 10, 'stream': stream}
        if tool:
            payload['tools'] = [{'name': 'get_weather', 'input_schema': schema}]
        headers = {'x-api-key': KEY, 'anthropic-version': '2023-06-01'}
    else:
        path = '/v1beta/models/' + model + (':streamGenerateContent?alt=sse' if stream else ':generateContent')
        payload = {'contents': [{'role': 'user', 'parts': [{'text': 'synthetic fixture'}]}],
                   'generationConfig': {'maxOutputTokens': 10}}
        if tool:
            payload['tools'] = [{'functionDeclarations': [{'name': 'get_weather', 'parameters': schema}]}]
        headers = {'x-goog-api-key': KEY}
    status, raw = request(path, payload, headers=headers)
    assert status == status_expected, f'HTTP {status}, expected {status_expected}'
    if status == 200:
        assert ('get_weather' if tool else 'mock-ok') in raw, 'missing expected content or function name'
        if tool:
            assert 'Paris' in raw, 'missing tool argument'
        if stream:
            events = [json.loads(line[6:]) for line in raw.splitlines()
                      if line.startswith('data: ') and line != 'data: [DONE]']
            assert events, 'missing SSE data'
            marker = {'chat': '[DONE]', 'responses': 'response.completed', 'messages': 'message_stop'}.get(protocol)
            if marker:
                assert marker in raw, 'missing stream terminal event'
        else:
            events = [json.loads(raw)]
        usages = []
        for event in events:
            for obj in [event, event.get('response', {}), event.get('message', {})]:
                for key in ['usage', 'usageMetadata']:
                    if isinstance(obj.get(key), dict):
                        usages.append(obj[key])
        assert any(any(u.get(k) == 10 for k in ['prompt_tokens', 'input_tokens', 'promptTokenCount']) for u in usages), 'input usage mismatch'
        assert any(any(u.get(k) == 5 for k in ['completion_tokens', 'output_tokens', 'candidatesTokenCount']) for u in usages), 'output usage mismatch'
    if model == 'mock-fallback':
        with urllib.request.urlopen('http://mock:8000/events', timeout=2) as response:
            modes = [event['mode'] for event in json.load(response)]
        assert modes == ['primary', 'backup'], f'unexpected fallback order {modes}'
    expected = 20 if status == 200 else 0
    deadline = time.monotonic() + 5
    while True:
        after = snapshot()
        delta = before['quota'] - after['quota']
        if delta == expected:
            break
        assert time.monotonic() < deadline, f'ledger delta {delta}, expected {expected}'
        time.sleep(.05)
    return {'http': status, 'debit': expected, 'route': 'native-openai' if protocol in ('chat', 'responses') else 'converted-via-openai'}


if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('--inspect', action='store_true')
    p.add_argument('--restore-probe', action='store_true')
    args = p.parse_args()
    password = os.environ['TEST_ADMIN_PASSWORD']
    setup = api('/api/setup')
    if not args.inspect and not args.restore_probe:
        assert not setup['status'], 'fresh test volume required; existing data is never overwritten'
        api('/api/setup', {'username': 'phase2root', 'password': password, 'confirmPassword': password,
                          'SelfUseModeEnabled': True, 'DemoSiteEnabled': False})
    login = api('/api/user/login', {'username': 'phase2root', 'password': password})
    AUTH = login['access_token']
    models = ['mock-text', 'mock-tool', 'mock-rate', 'mock-error', 'mock-timeout', 'mock-fallback']
    if not args.inspect and not args.restore_probe:
        for k, v in {'RegisterEnabled': 'false', 'PasswordResetEnabled': 'false', 'RetryTimes': '0',
                     'ModelRatio': json.dumps(dict.fromkeys(models, 1)),
                     'CompletionRatio': json.dumps(dict.fromkeys(models, 2)), 'GroupRatio': '{"default":1}'}.items():
            api('/api/option/', {'key': k, 'value': v}, 'PUT')
        for model, mode, priority in [('mock-text', 'ok', 10), ('mock-tool', 'ok', 10), ('mock-rate', 'rate', 10),
                ('mock-error', 'error', 10), ('mock-timeout', 'timeout', 10),
                ('mock-fallback', 'primary', 10), ('mock-fallback', 'backup', 1)]:
            api('/api/channel/', {'mode': 'single', 'channel': {'type': 1, 'name': model + '-' + mode,
                'key': 'mock-only-not-a-real-key', 'models': model, 'group': 'default',
                'base_url': 'http://mock:8000/' + mode, 'status': 1, 'priority': priority, 'weight': 1}}, 'POST')
        api('/api/token/', {'name': 'phase2-fixture', 'expired_time': -1, 'remain_quota': 1000000,
                           'model_limits_enabled': True, 'model_limits': ','.join(models), 'group': 'default'}, 'POST')
    token = items(api('/api/token/?p=1&page_size=100'))[0]
    KEY = 'sk-' + api(f"/api/token/{token['id']}/key", {}, 'POST')['key']
    if args.inspect:
        result = snapshot()
        result['fingerprint'] = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
        print(json.dumps(result))
    elif args.restore_probe:
        check('restored-chat-call', lambda: protocol_case('chat'))
        print(json.dumps({'passed': sum(r['status'] == 'PASS' for r in ROWS), 'total': len(ROWS)}))
        raise SystemExit(0 if all(r['status'] == 'PASS' for r in ROWS) else 1)
    else:
        for protocol in ['chat', 'responses', 'messages', 'gemini']:
            for stream, tool in [(False, False), (True, False), (False, True), (True, True)]:
                check(f'{protocol}-stream={stream}-tool={tool}', lambda p=protocol, s=stream, t=tool: protocol_case(p, s, t))
        for model, status in [('mock-rate', 429), ('mock-error', 500), ('mock-timeout', 500)]:
            check(model, lambda m=model, st=status: protocol_case('chat', model=m, status_expected=st))
        api('/api/option/', {'key': 'RetryTimes', 'value': '1'}, 'PUT')
        check('chat-failover', lambda: protocol_case('chat', model='mock-fallback'))
        print(json.dumps({'passed': sum(r['status'] == 'PASS' for r in ROWS), 'total': len(ROWS), 'results': ROWS, 'ledger': snapshot()}))
        raise SystemExit(0 if all(r['status'] == 'PASS' for r in ROWS) else 1)
