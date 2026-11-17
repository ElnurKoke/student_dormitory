/* NCALayer basics.sign protocol. No key files, passwords or certificate claims
 * are read by this page. The server verifies the CMS and the one-time challenge. */
(() => {
  "use strict";
  const form = document.getElementById("eds-login");
  if (!form) return;
  const status = document.getElementById("ncalayer-status");
  const error = document.getElementById("eds-error");
  const check = document.getElementById("ncalayer-check");
  const submit = document.getElementById("eds-submit");
  const text = (key) => form.dataset[key];
  let socket, connecting, pending;
  let ready = false;
  let busy = false;

  class SessionExpiredError extends Error {}
  const failure = (key) => new Error(text(key));
  const showError = (message) => { error.textContent = message; error.hidden = false; };
  const setBusy = (value) => {
    busy = value;
    submit.disabled = value;
    check.disabled = value;
    form.setAttribute("aria-busy", String(value));
  };

  function connect() {
    if (ready && socket && socket.readyState === WebSocket.OPEN) return Promise.resolve();
    if (connecting) return connecting;
    status.textContent = text("msgChecking");
    const attempt = new Promise((resolve, reject) => {
      let ws;
      try { ws = new WebSocket("wss://127.0.0.1:13579/"); }
      catch { status.textContent = text("msgDisconnected"); reject(failure("msgDisconnected")); return; }
      socket = ws;
      let settled = false;
      const timer = setTimeout(() => { fail(); ws.close(); }, 5000);
      function fail() {
        clearTimeout(timer);
        if (socket === ws) {
          ready = false;
          status.textContent = text("msgDisconnected");
          if (pending) pending.reject(failure("msgDisconnected"));
        }
        if (!settled) { settled = true; reject(failure("msgDisconnected")); }
      }
      ws.onerror = () => { fail(); ws.close(); };
      ws.onclose = fail;
      ws.onmessage = (event) => {
        let response;
        try { response = JSON.parse(event.data); } catch { fail(); ws.close(); return; }
        if (!settled && response && response.result && typeof response.result.version === "string") {
          settled = true;
          clearTimeout(timer);
          ready = true;
          status.textContent = text("msgConnected");
          resolve();
          return;
        }
        if (socket !== ws || !pending || !response || typeof response.status !== "boolean") return;
        if (!response.status) { pending.reject(failure("msgSignError")); return; }
        let signature = response.body && response.body.result;
        if (signature === undefined || signature === null) { pending.reject(failure("msgCancelled")); return; }
        if (Array.isArray(signature)) signature = signature.length === 1 ? signature[0] : null;
        if (typeof signature !== "string" || !signature || signature.length > 32768) {
          pending.reject(failure("msgSignError"));
          return;
        }
        pending.resolve(signature);
      };
    });
    connecting = attempt;
    attempt.then(() => { connecting = null; }, () => { connecting = null; });
    return attempt;
  }

  function sign(data) {
    return new Promise((resolve, reject) => {
      const finish = (callback, value) => { clearTimeout(timer); pending = null; callback(value); };
      const timer = setTimeout(() => {
        if (pending) pending.reject(failure("msgSignError"));
        socket.close();
      }, 180000);
      pending = { resolve: (value) => finish(resolve, value), reject: (value) => finish(reject, value) };
      try {
        socket.send(JSON.stringify({
          module: "kz.gov.pki.knca.basics",
          method: "sign",
          args: {
            format: "cms", data,
            signingParams: { decode: true, encapsulate: true, digested: false },
            signerParams: { extKeyUsageOids: [] }, locale: form.dataset.locale,
          },
        }));
      } catch { pending.reject(failure("msgDisconnected")); }
    });
  }

  async function post(url, values) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 30000);
    try {
      let response;
      try { response = await fetch(url, {
        method: "POST", credentials: "same-origin", mode: "same-origin", redirect: "error", cache: "no-store",
        headers: { "Accept": "application/json" },
        body: new URLSearchParams({ csrf_token: form.elements.csrf_token.value, ...values }),
        signal: controller.signal,
      }); } catch { throw failure("msgServerError"); }
      if (!response.ok && (response.status === 401 || response.status === 403) &&
          (url === form.dataset.challengeUrl || !(response.headers.get("Content-Type") || "").includes("application/json"))) {
        throw new SessionExpiredError(text("msgSessionExpired"));
      }
      let result;
      try { result = await response.json(); } catch { throw failure("msgServerError"); }
      if (!result || typeof result !== "object") throw failure("msgServerError");
      if (!response.ok) throw new Error(typeof result.message === "string" ? result.message : text("msgServerError"));
      return result;
    } finally { clearTimeout(timer); }
  }

  async function loginChallenge() {
    try { return await post(form.dataset.challengeUrl, {}); }
    catch (err) { if (!(err instanceof SessionExpiredError)) throw err; }
    // Refresh through the normal login page. Keep server-side CSRF checks and
    // never replay a signature after a session expires during key selection.
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 30000);
    try {
      let response;
      try { response = await fetch(form.dataset.sessionUrl, {
        method: "GET", credentials: "same-origin", mode: "same-origin", redirect: "error", cache: "no-store", signal: controller.signal,
      }); } catch { throw failure("msgServerError"); }
      if (!response.ok) throw failure("msgServerError");
      const page = new DOMParser().parseFromString(await response.text(), "text/html");
      const token = page.querySelector('#eds-login input[name="csrf_token"]')?.value;
      if (typeof token !== "string" || !token || token.length > 256) throw failure("msgServerError");
      for (const field of document.querySelectorAll('input[name="csrf_token"]')) field.value = token;
    } finally { clearTimeout(timer); }
    return post(form.dataset.challengeUrl, {});
  }

  check.addEventListener("click", async () => {
    if (busy) return;
    setBusy(true);
    error.hidden = true;
    try { await connect(); } catch { showError(text("msgDisconnected")); }
    finally { setBusy(false); }
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    error.hidden = true;
    try {
      await connect();
      const challenge = await loginChallenge();
      if (typeof challenge.data !== "string" || !challenge.data || !Number.isFinite(challenge.expires_at) || challenge.expires_at <= Date.now() / 1000) throw failure("msgServerError");
      status.textContent = text("msgSigning");
      const cms = await sign(challenge.data);
      status.textContent = text("msgVerifying");
      const result = await post(form.action, { cms });
      if (result.redirect !== "/account") throw failure("msgServerError");
      window.location.assign(form.dataset.accountUrl);
    } catch (err) {
      showError(err instanceof Error && err.message ? err.message : text("msgServerError"));
      status.textContent = text(ready ? "msgConnected" : "msgDisconnected");
    } finally { setBusy(false); }
  });

  window.addEventListener("pagehide", () => { if (socket) socket.close(); });
  setBusy(false);
  connect().catch(() => { /* The status explains how to retry after starting NCALayer. */ });
})();
