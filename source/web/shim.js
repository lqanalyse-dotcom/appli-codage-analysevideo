// Pont entre le dashboard et l'application native : remplace les capacités claude.ai
// (base de matchs, fichiers, téléchargements) par le stockage sur le disque de l'ordinateur.
(function () {
  "use strict";
  window.__NATIVE__ = true;
  const clone = (o) => (o == null ? o : JSON.parse(JSON.stringify(o)));
  const subs = new Set();
  const notify = () => setTimeout(() => subs.forEach((f) => f()), 0);
  async function api(method, url, body, headers) {
    const r = await fetch(url, { method, body, headers });
    if (!r.ok) {
      const t = await r.text().catch(() => "");
      throw { code: r.status === 413 ? "too_large" : "unavailable", message: t || "Erreur " + r.status };
    }
    const ct = r.headers.get("content-type") || "";
    return ct.includes("json") ? r.json() : r.text();
  }
  const q = (p) => "?path=" + encodeURIComponent(p);
  function docRef(path) {
    const id = path.split("/").pop();
    return {
      id, path,
      async get() { const d = await api("GET", "/api/doc" + q(path)); return { id, exists: d.exists, data: () => clone(d.data) }; },
      async set(data) { await api("PUT", "/api/doc" + q(path), JSON.stringify(data), { "Content-Type": "application/json" }); notify(); },
      async update(data) { const cur = await this.get(); if (!cur.exists) throw { code: "invalid_argument", message: "Document absent" }; await this.set(Object.assign(cur.data(), data)); },
      async delete() { await api("DELETE", "/api/doc" + q(path)); notify(); },
      onSnapshot(next, err) {
        let alive = true;
        const f = () => this.get().then((s) => alive && next(s)).catch((e) => err && err(e));
        subs.add(f); f();
        return () => { alive = false; subs.delete(f); };
      },
      collection(sub) { return colRef(path + "/" + sub); },
    };
  }
  function colRef(path) {
    const get = async () => {
      const list = await api("GET", "/api/col" + q(path));
      const docs = list.map((d) => ({ id: d.id, exists: true, data: () => clone(d.data) }));
      return { docs, size: docs.length, empty: !docs.length, docChanges: () => [], metadata: {} };
    };
    return {
      path,
      doc(id) { return docRef(path + "/" + (id || Math.random().toString(36).slice(2, 12))); },
      async add(data) { const ref = this.doc(); await ref.set(data); return ref; },
      get,
      where() { return this; }, orderBy() { return this; }, limit() { return this; },
      onSnapshot(next, err) {
        let alive = true;
        const f = () => get().then((s) => alive && next(s)).catch((e) => err && err(e));
        subs.add(f); f();
        return () => { alive = false; subs.delete(f); };
      },
    };
  }
  const db = { doc: docRef, collection: colRef };
  const assets = {
    async upload(blob, opts) {
      const type = (opts && opts.type) || blob.type || "application/octet-stream";
      return api("POST", "/api/blob", blob, { "X-Type": type });
    },
    async list() { return api("GET", "/api/blobs"); },
    async delete(ref) { const id = String(ref).split("/").pop(); return api("DELETE", "/api/blob/" + encodeURIComponent(id)); },
  };
  const downloads = {
    async save({ filename, data }) {
      const body = data instanceof Blob ? data : new Blob([data]);
      const r = await api("POST", "/api/save?name=" + encodeURIComponent(filename), body);
      return { status: "saved", path: r.path };
    },
  };
  // Assistant Claude : boucle d'outils côté page, chaque tour passe par le serveur local (clé API sur l'ordinateur)
  const MODEL = "claude-opus-5-5";
  async function sample(input, opts) {
    opts = opts || {};
    const tools = opts.tools || [];
    const defs = tools.map((t) => ({ name: t.name, description: t.description || "", input_schema: t.inputSchema || { type: "object", properties: {} } }));
    const messages = input.map((m) => ({ role: m.role, content: m.content }));
    let text = "";
    for (let turn = 0; turn < 16; turn++) {
      let r;
      try {
        r = await fetch("/api/claude", { method: "POST", signal: opts.signal, headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ model: MODEL, max_tokens: 16000, messages, tools: defs.length ? defs : undefined, fallbacks: "default" }) });
      } catch (e) {
        if (e && e.name === "AbortError") throw { code: "cancelled", text };
        throw { code: "network", message: "serveur local injoignable", text };
      }
      const res = await r.json().catch(() => ({}));
      if (!r.ok) throw { code: res.code || "api_error", message: res.message || "Erreur " + r.status, text };
      if (res.stop_reason === "refusal") throw { code: "refusal", message: "Claude a refusé cette demande.", text };
      const said = (res.content || []).filter((b) => b.type === "text").map((b) => b.text).join("");
      if (said) { text = text ? text + "\n\n" + said : said; if (opts.onText) opts.onText({ text }); }
      if (res.stop_reason !== "tool_use") return { text };
      messages.push({ role: "assistant", content: res.content });
      const results = [];
      for (const b of res.content.filter((x) => x.type === "tool_use")) {
        const tool = tools.find((t) => t.name === b.name);
        try {
          if (!tool) throw new Error("outil inconnu");
          const out = await tool.execute(b.input || {});
          results.push({ type: "tool_result", tool_use_id: b.id, content: JSON.stringify(out === undefined ? { ok: true } : out) });
        } catch (e) {
          results.push({ type: "tool_result", tool_use_id: b.id, content: String((e && e.message) || e), is_error: true });
        }
      }
      messages.push({ role: "user", content: results });
    }
    return { text: text || "J'ai atteint la limite d'étapes pour cette demande." };
  }
  sample.native = true;
  sample.keyStatus = () => api("GET", "/api/claude/key");
  sample.setKey = (key) => api("POST", "/api/claude/key", JSON.stringify({ key }), { "Content-Type": "application/json" });
  sample.clearKey = () => api("DELETE", "/api/claude/key");
  const caps = { db, assets, downloads, sample };
  window.claude = { use: async (name) => caps[name] || null };
  // Signal de vie : l'application se ferme seule quand la fenêtre est fermée
  // Mise à jour téléchargée en arrière-plan : on propose de relancer
  const checkUpd = () => fetch("/api/update").then((r) => r.json()).then((u) => {
    if (!u.ready || document.getElementById("maj-banner")) return;
    const d = document.createElement("div"); d.id = "maj-banner"; d.setAttribute("role", "status");
    d.style.cssText = "position:fixed;left:16px;bottom:16px;z-index:9999;background:#0a1f4f;color:#fff;padding:10px 14px;border-radius:10px;font:600 14px Barlow,system-ui,sans-serif;box-shadow:0 6px 20px rgba(0,0,0,.25);display:flex;gap:10px;align-items:center";
    d.innerHTML = 'Nouvelle version disponible <button style="background:#f6c400;color:#0a1f4f;border:0;border-radius:6px;padding:6px 10px;font-weight:700;cursor:pointer">Recharger</button>';
    d.querySelector("button").onclick = () => location.reload(); document.body.appendChild(d);
  }).catch(() => {});
  setTimeout(checkUpd, 15000); setInterval(checkUpd, 120000);
  const ping = () => fetch("/api/ping", { method: "POST" }).catch(() => {});
  ping(); setInterval(ping, 4000);
})();
