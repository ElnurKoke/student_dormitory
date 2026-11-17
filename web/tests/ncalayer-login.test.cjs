const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../static/ncalayer-login.js'), 'utf8');

function harness({ cancelled = false, unavailable = false, signatures, serverFailure = false, closeOnSign = false, blocked = false, expiredChallenge = false, expiredLogin = false } = {}) {
  const elements = new Map();
  function element(id) {
    const item = { listeners: {}, dataset: {}, hidden: false, disabled: false, textContent: '',
      addEventListener(name, fn) { this.listeners[name] = fn; },
      setAttribute(name, value) { this[name] = value; },
      set innerHTML(_) { throw new Error('untrusted HTML insertion'); },
    };
    elements.set(id, item);
    return item;
  }
  for (const id of ['eds-login','ncalayer-status','eds-error','ncalayer-check','eds-submit']) element(id);
  const form = elements.get('eds-login');
  form.action = '/auth/eds/login?lang=kk';
  form.elements = { csrf_token: { value: 'server-csrf-token' } };
  form.dataset = { sessionUrl: '/auth/login?lang=kk', challengeUrl: '/auth/eds/challenge?lang=kk', accountUrl: '/account?lang=kk', locale: 'kk',
    msgChecking: 'checking', msgConnected: 'connected', msgDisconnected: 'disconnected', msgSigning: 'signing',
    msgSessionExpired: 'session-expired', msgVerifying: 'verifying', msgCancelled: 'cancelled', msgSignError: 'sign-error', msgServerError: 'server-error' };
  let challengeCalls = 0;
  const requests = [], signing = [], sockets = [], windowEvents = {}, redirects = [];
  class FakeWebSocket {
    static OPEN = 1;
    constructor(url) {
      if (blocked) throw new Error("browser blocked local network");
      assert.equal(url,'wss://127.0.0.1:13579/');
      this.readyState = 1;
      sockets.push(this);
      queueMicrotask(() => unavailable ? this.close() : this.onmessage({ data: JSON.stringify({ result: { version: 'fixture' } }) }));
    }
    send(data) {
      const request = JSON.parse(data);
      signing.push(request);
      // NCALayer requires extKeyUsageOids even when no EKU filter is wanted.
      // Its rejection happens before the native file/password dialog appears.
      if (!request.args.signerParams || !Array.isArray(request.args.signerParams.extKeyUsageOids)) {
        queueMicrotask(() => this.onmessage({ data: JSON.stringify({ status: false, code: 'SIGNER_PARAMS_ERROR' }) }));
        return;
      }
      queueMicrotask(() => {
        if (closeOnSign) { this.close(); return; }
        this.onmessage({ data: JSON.stringify(cancelled ? { status: true, body: {} } : { status: true, body: { result: signatures || ['CMS-FIXTURE'] } }) });
      });
    }
    close() { this.readyState = 3; if (this.onclose) this.onclose(); }
  }
  const context = { document: { getElementById: (id) => elements.get(id), querySelectorAll: () => [form.elements.csrf_token] },
    DOMParser: class { parseFromString(html, type) { assert.equal(type, 'text/html'); assert.equal(html, 'FRESH-LOGIN-PAGE'); return { querySelector: () => ({ value: 'fresh-server-csrf-token' }) }; } },
    window: { addEventListener(name, fn) { windowEvents[name] = fn; }, location: { assign(url) { redirects.push(url); } } },
    WebSocket: FakeWebSocket, URLSearchParams, AbortController, setTimeout, clearTimeout,
    fetch: async (url, options) => {
      requests.push({ url, options, body: options.body ? Object.fromEntries(options.body) : {} });
      const htmlSessionExpired = { ok: false, status: 401, headers: { get: () => 'text/html' }, json: async () => { throw new Error('HTML response'); } };
      if (url === form.dataset.sessionUrl) return { ok: true, text: async () => 'FRESH-LOGIN-PAGE' };
      if (url.startsWith('/auth/eds/challenge') && expiredChallenge && challengeCalls++ === 0) return htmlSessionExpired;
      if (url.startsWith('/auth/eds/login') && expiredLogin) return htmlSessionExpired;
      if (url.startsWith('/auth/eds/challenge')) return { ok: true, json: async () => ({ data: 'CHALLENGE-BASE64', expires_at: Date.now()/1000+300 }) };
      if (serverFailure) return { ok: false, json: async () => ({ message: '<img src=x onerror=alert(1)>' }) };
      return { ok: true, json: async () => ({ redirect: '/account' }) };
    },
  };
  vm.runInNewContext(source, context);
  return { elements, requests, signing, redirects, async submit() { await form.listeners.submit({ preventDefault() {} }); },
    cleanup() { windowEvents.pagehide(); } };
}

test('NCALayer readiness, attached CMS, CSRF and successful localized login', async () => {
  const h = harness();
  try {
    await new Promise(setImmediate);
    assert.equal(h.elements.get('ncalayer-status').textContent,'connected');
    await h.submit();
    assert.deepEqual(h.redirects,['/account?lang=kk']);
    assert.equal(h.signing.length,1);
    const request = h.signing[0];
    assert.equal(request.module,'kz.gov.pki.knca.basics');
    assert.equal(request.method,'sign');
    assert.equal(request.args.format,'cms');
    assert.equal(request.args.signingParams.encapsulate,true);
    assert.equal(request.args.signingParams.decode,true);
    assert.equal(request.args.signingParams.digested,false);
    assert.equal(request.args.locale,'kk');
    assert.deepEqual(request.args.signerParams,{ extKeyUsageOids: [] });
    assert.equal(Object.hasOwn(request.args.signerParams, 'chain'),false);
    assert.equal(h.requests.length,2);
    assert.equal(h.requests[0].body.csrf_token,'server-csrf-token');
    assert.deepEqual(h.requests[1].body,{ csrf_token: 'server-csrf-token', cms: 'CMS-FIXTURE' });
    assert.equal(h.requests[1].options.credentials,'same-origin');
    assert.equal(h.requests[1].options.redirect,'error');
    assert.equal(h.elements.get('eds-submit').disabled,false);
  } finally { h.cleanup(); }
});

for (const [name, options, expectedError] of [
  ['cancellation',{cancelled:true},'cancelled'],
  ['multiple signatures',{signatures:['one','two']},'sign-error'],
  ['socket closes during signing',{closeOnSign:true},'disconnected'],
  ['NCALayer unavailable',{unavailable:true},'disconnected'],
  ['browser blocks WebSocket',{blocked:true},'disconnected'],
]) test(name+' does not send a login proof',async()=>{
  const h=harness(options);
  try {
    await h.submit();
    assert.equal(h.requests.filter(r=>r.url.startsWith('/auth/eds/login')).length,0);
    assert.equal(h.elements.get('eds-error').textContent,expectedError);
    assert.equal(h.elements.get('eds-error').hidden,false);
    assert.equal(h.elements.get('eds-submit').disabled,false);
    assert.equal(h.redirects.length,0);
  } finally {h.cleanup();}
});

test('server error is shown as text and allows retry',async()=>{
  const h=harness({serverFailure:true});
  try {
    await h.submit();
    assert.equal(h.elements.get('eds-error').textContent,'<img src=x onerror=alert(1)>');
    assert.equal(h.redirects.length,0);
    assert.equal(h.elements.get('eds-submit').disabled,false);
  } finally {h.cleanup();}
});

test('expired anonymous session is refreshed before opening the key dialog', async () => {
  const h = harness({ expiredChallenge: true });
  try {
    await h.submit();
    assert.deepEqual(h.requests.map(r => r.url), [
      '/auth/eds/challenge?lang=kk', '/auth/login?lang=kk', '/auth/eds/challenge?lang=kk', '/auth/eds/login?lang=kk',
    ]);
    assert.equal(h.requests[2].body.csrf_token, 'fresh-server-csrf-token');
    assert.equal(h.requests[3].body.csrf_token, 'fresh-server-csrf-token');
    assert.equal(h.signing.length, 1);
    assert.deepEqual(h.redirects, ['/account?lang=kk']);
  } finally { h.cleanup(); }
});

test('session expiration after signing does not replay a signature or reopen key selection', async () => {
  const h = harness({ expiredLogin: true });
  try {
    await h.submit();
    assert.equal(h.elements.get('eds-error').textContent, 'session-expired');
    assert.equal(h.signing.length, 1);
    assert.equal(h.requests.length, 2);
    assert.equal(h.redirects.length, 0);
  } finally { h.cleanup(); }
});
