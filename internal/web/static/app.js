const $ = s => document.querySelector(s);
const $$ = s => document.querySelectorAll(s);

let me = null;
let curPath = "";

function toast(msg, err) {
  const t = $("#toast");
  t.textContent = msg;
  t.classList.toggle("err", !!err);
  t.classList.remove("hidden");
  clearTimeout(t._h);
  t._h = setTimeout(() => t.classList.add("hidden"), 4000);
}

async function api(url, opts) {
  const resp = await fetch(url, opts);
  let data = null;
  try { data = await resp.json(); } catch (e) {}
  if (resp.status === 401 && data && data.error && !url.includes("/api/login")) {
    showLogin();
    throw new Error(data.error || "не авторизован");
  }
  if (!resp.ok) throw new Error((data && data.error) || ("HTTP " + resp.status));
  return data;
}

function post(url, body) {
  return api(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
}

let publicBaseP = null;
function publicOrigin() {
  if (!publicBaseP) {
    publicBaseP = api("/api/base")
      .then(d => d.public_base || location.origin)
      .catch(() => location.origin);
  }
  return publicBaseP;
}

async function copyText(text) {
  try {
    if (navigator.clipboard && navigator.clipboard.writeText && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch (e) {}
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.cssText = "position:fixed;opacity:0;";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return !!ok;
  } catch (e) { return false; }
}

function human(n) {
  if (n == null) return "—";
  if (n < 1024) return n + " Б";
  const u = ["КБ", "МБ", "ГБ", "ТБ"];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(1) + " " + u[i];
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, c => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function showLogin() {
  $("#login-view").classList.remove("hidden");
  $("#app-view").classList.add("hidden");
}

function showApp() {
  $("#login-view").classList.add("hidden");
  $("#app-view").classList.remove("hidden");
  $("#whoami").textContent = me.username + (me.admin ? " (админ)" : "");
  $$(".admin-only").forEach(el => el.classList.toggle("hidden", !me.admin));
  loadFiles();
}

$("#login-form").addEventListener("submit", async e => {
  e.preventDefault();
  $("#login-error").textContent = "";
  try {
    me = await post("/api/login", {
      username: $("#login-user").value.trim(),
      password: $("#login-pass").value,
    });
    showApp();
  } catch (err) {
    $("#login-error").textContent = err.message;
  }
});

$("#logout-btn").addEventListener("click", async () => {
  try { await post("/api/logout"); } catch (e) {}
  me = null;
  showLogin();
});

$$("#nav a").forEach(a => {
  a.addEventListener("click", e => {
    e.preventDefault();
    $$("#nav a").forEach(x => x.classList.remove("active"));
    a.classList.add("active");
    $$(".view").forEach(v => v.classList.add("hidden"));
    $("#view-" + a.dataset.view).classList.remove("hidden");
    if (a.dataset.view === "files") loadFiles();
    if (a.dataset.view === "shares") loadShares();
    if (a.dataset.view === "users") loadUsers();
    if (a.dataset.view === "settings") { loadSettings(); loadMounts(); }
    if (a.dataset.view === "net") loadNet();
  });
});

// ---------- FILES ----------

function crumbHtml(path) {
  const parts = path ? path.split("/") : [];
  let html = '<a href="#" data-p="">📁 корень</a>';
  let acc = "";
  for (const p of parts) {
    acc = acc ? acc + "/" + p : p;
    html += ' / <a href="#" data-p="' + esc(acc) + '">' + esc(p) + "</a>";
  }
  return html;
}

async function loadFiles(path) {
  if (path !== undefined) curPath = path;
  let data;
  try {
    data = await api("/api/fs?path=" + encodeURIComponent(curPath));
  } catch (err) { toast(err.message, true); return; }
  curPath = data.path;
  $("#breadcrumb").innerHTML = crumbHtml(curPath);
  $$("#breadcrumb a").forEach(a => a.addEventListener("click", e => {
    e.preventDefault();
    loadFiles(a.dataset.p);
  }));
  const tb = $("#fs-body");
  tb.innerHTML = "";
  $("#btn-dl-dir").classList.toggle("hidden", !curPath || !!data.virtual_root);
  $("#fs-empty").classList.toggle("hidden", data.entries.length > 0);
  for (const en of data.entries) {
    const tr = document.createElement("tr");
    const icon = en.is_dir ? "📁" : "📄";
    tr.innerHTML =
      '<td><span class="icon">' + icon + '</span>' +
      (en.is_dir
        ? '<a class="name-link" data-open="' + esc(en.path) + '">' + esc(en.name) + "</a>"
        : '<span class="name-link">' + esc(en.name) + "</span>") +
      "</td>" +
      '<td class="num">' + (en.is_dir ? "—" : human(en.size)) + "</td>" +
      "<td>" + esc(en.modtime) + "</td>" +
      '<td class="acts">' +
      (en.is_dir
        ? '<button class="btn small" data-dl="' + esc(en.path) + '">Скачать</button>'
        : '<a class="btn small" href="/download?path=' + encodeURIComponent(en.path) + '">Скачать</a>') +
      '<button class="btn small" data-share="' + esc(en.path) + '">Ссылка</button>' +
      '<button class="btn small" data-ren="' + esc(en.path) + '">Переим.</button>' +
      '<button class="btn small danger" data-del="' + esc(en.path) + '">Удалить</button>' +
      "</td>";
    tb.appendChild(tr);
  }

  tb.querySelectorAll("[data-open]").forEach(a =>
    a.addEventListener("click", () => loadFiles(a.dataset.open)));
  tb.querySelectorAll("[data-dl]").forEach(b =>
    b.addEventListener("click", () => downloadFolder(b.dataset.dl)));
  tb.querySelectorAll("[data-share]").forEach(b =>
    b.addEventListener("click", () => quickShare(b.dataset.share)));
  tb.querySelectorAll("[data-ren]").forEach(b =>
    b.addEventListener("click", () => renameEntry(b.dataset.ren)));
  tb.querySelectorAll("[data-del]").forEach(b =>
    b.addEventListener("click", () => deleteEntry(b.dataset.del)));
}

function parentOf(p) {
  if (!p) return "";
  const i = p.lastIndexOf("/");
  return i < 0 ? "" : p.slice(0, i);
}

$("#btn-up").addEventListener("click", () => loadFiles(parentOf(curPath)));
$("#btn-refresh") && null;

$("#btn-mkdir").addEventListener("click", async () => {
  const name = prompt("Имя новой папки:");
  if (!name) return;
  const path = curPath ? curPath + "/" + name : name;
  try {
    await post("/api/mkdir", { path });
    loadFiles();
  } catch (err) { toast(err.message, true); }
});

$("#btn-upload").addEventListener("click", () => $("#file-input").click());
$("#file-input").addEventListener("change", e => uploadFiles(e.target.files));

async function uploadFiles(fileList) {
  if (!fileList || !fileList.length) return;
  const fd = new FormData();
  for (const f of fileList) fd.append("files", f, f.name);
  toast("Загрузка " + fileList.length + " файл(ов)…");
  try {
    const r = await api("/api/upload?path=" + encodeURIComponent(curPath), { method: "POST", body: fd });
    toast("Загружено: " + r.saved);
    loadFiles();
  } catch (err) { toast(err.message, true); }
}

const dz = $("#dropzone");
["dragenter", "dragover"].forEach(ev => dz.addEventListener(ev, e => {
  e.preventDefault(); dz.classList.add("drag");
}));
["dragleave", "drop"].forEach(ev => dz.addEventListener(ev, e => {
  e.preventDefault(); dz.classList.remove("drag");
}));
dz.addEventListener("drop", e => uploadFiles(e.dataTransfer.files));

function downloadFolder(rel) {
  torrentDownload({
    label: rel || "папка",
    manifestUrl: "/api/manifest?hash=0&path=" + encodeURIComponent(rel),
    fileUrl: r => "/download?path=" + encodeURIComponent(rel ? rel + "/" + r : r),
  });
}

$("#btn-dl-dir").addEventListener("click", () => downloadFolder(curPath));

async function quickShare(path) {
  try {
    const sh = await post("/api/shares", { path, name: path.split("/").pop() });
    const link = (await publicOrigin()) + "/s/" + sh.token;
    const ok = await copyText(link);
    toast((ok ? "Ссылка скопирована: " : "Скопируйте вручную: ") + link);
    loadShares();
  } catch (err) { toast(err.message, true); }
}

async function renameEntry(path) {
  const base = path.split("/").pop();
  const nn = prompt("Новое имя:", base);
  if (!nn || nn === base) return;
  const dir = parentOf(path);
  try {
    await post("/api/rename", { from: path, to: dir ? dir + "/" + nn : nn });
    loadFiles();
  } catch (err) { toast(err.message, true); }
}

async function deleteEntry(path) {
  if (!confirm("Удалить «" + path + "» безвозвратно?")) return;
  try {
    await post("/api/delete", { path });
    loadFiles();
  } catch (err) { toast(err.message, true); }
}

// ---------- SHARES ----------

async function loadShares() {
  const origin = await publicOrigin();
  $("#dl-example").textContent = "fileshare download " + origin + "/s/ТОКЕН -o Games";
  let list;
  try { list = await api("/api/shares"); } catch (err) { toast(err.message, true); return; }
  const tb = $("#shares-body");
  tb.innerHTML = "";
  if (!list.length) {
    tb.innerHTML = '<tr><td colspan="5" class="hint">Ссылок пока нет</td></tr>';
    return;
  }
  for (const sh of list) {
    const link = origin + "/s/" + sh.token;
    const tr = document.createElement("tr");
    tr.innerHTML =
      "<td>" + esc(sh.name) + (sh.has_password ? ' <span class="badge">🔒</span>' : "") +
      (sh.expired ? ' <span class="badge">истекла</span>' : "") + "</td>" +
      "<td>" + esc(sh.path || "/") + "</td>" +
      '<td><code>' + esc(link) + "</code></td>" +
      "<td>" + (sh.expires ? esc(sh.expires.slice(0, 10)) : "бессрочно") + "</td>" +
      '<td class="acts">' +
      '<button class="btn small" data-copy="' + esc(link) + '">Копировать</button>' +
      '<button class="btn small danger" data-rm="' + esc(sh.token) + '">Удалить</button>' +
      "</td>";
    tb.appendChild(tr);
  }
  tb.querySelectorAll("[data-copy]").forEach(b => b.addEventListener("click", async () => {
    const link = b.dataset.copy;
    const ok = await copyText(link);
    if (ok) toast("Ссылка скопирована");
    else toast("Скопируйте вручную: " + link, true);
  }));
  tb.querySelectorAll("[data-rm]").forEach(b => b.addEventListener("click", async () => {
    if (!confirm("Отозвать ссылку?")) return;
    try {
      await api("/api/shares?token=" + b.dataset.rm, { method: "DELETE" });
      loadShares();
    } catch (err) { toast(err.message, true); }
  }));
}

$("#sh-create").addEventListener("click", async () => {
  const path = $("#sh-path").value.trim();
  if (!path) { toast("Укажите путь", true); return; }
  let exp = "";
  const expVal = $("#sh-exp").value;
  if (expVal) exp = new Date(expVal).toISOString();
  try {
    const sh = await post("/api/shares", {
      path,
      name: $("#sh-name").value.trim() || path.split("/").pop(),
      password: $("#sh-pass").value,
      expires: exp,
    });
    const link = (await publicOrigin()) + "/s/" + sh.token;
    const ok = await copyText(link);
    toast((ok ? "Создана и скопирована: " : "Создана. Скопируйте вручную: ") + link);
    $("#sh-path").value = ""; $("#sh-name").value = ""; $("#sh-pass").value = ""; $("#sh-exp").value = "";
    loadShares();
  } catch (err) { toast(err.message, true); }
});

// ---------- USERS ----------

async function loadUsers() {
  let list;
  try { list = await api("/api/users"); } catch (err) { toast(err.message, true); return; }
  const tb = $("#users-body");
  tb.innerHTML = "";
  for (const u of list) {
    const tr = document.createElement("tr");
    tr.innerHTML =
      "<td>" + esc(u.username) + "</td>" +
      "<td>" + (u.admin === "true" || u.admin === true ? "админ" : "пользователь") + "</td>" +
      '<td class="acts"><button class="btn small danger" data-del="' + esc(u.username) + '">Удалить</button></td>';
    tb.appendChild(tr);
  }
  tb.querySelectorAll("[data-del]").forEach(b => b.addEventListener("click", async () => {
    if (!confirm("Удалить пользователя " + b.dataset.del + "?")) return;
    try {
      await api("/api/users?username=" + encodeURIComponent(b.dataset.del), { method: "DELETE" });
      loadUsers();
    } catch (err) { toast(err.message, true); }
  }));
}

$("#u-create").addEventListener("click", async () => {
  try {
    await post("/api/users", {
      username: $("#u-name").value.trim(),
      password: $("#u-pass").value,
      admin: $("#u-admin").checked,
    });
    $("#u-name").value = ""; $("#u-pass").value = ""; $("#u-admin").checked = false;
    toast("Пользователь добавлен");
    loadUsers();
  } catch (err) { toast(err.message, true); }
});

// ---------- SETTINGS ----------

async function loadSettings() {
  let s;
  try { s = await api("/api/settings"); } catch (err) { toast(err.message, true); return; }
  $("#set-web").value = s.web_port;
  $("#set-sftp").value = s.sftp_port;
  $("#set-bind").value = s.bind;
  $("#set-root").value = s.root_dir;
  $("#set-https").checked = !!s.https;
  $("#set-zip").value = s.zip_max_bytes;
  $("#set-ip").value = s.external_ip || "";
  $("#set-ttl").value = s.session_ttl_minutes;
  $("#cert-info").textContent = s.cert_info || "";
}

async function loadMounts() {
  let list;
  try { list = await api("/api/mounts"); } catch (err) { toast(err.message, true); return; }
  const tb = $("#mounts-body");
  tb.innerHTML = "";
  if (!list.length) {
    tb.innerHTML = '<tr><td colspan="3" class="hint">Пока только корень программы — добавьте первую папку ниже</td></tr>';
    return;
  }
  for (const m of list) {
    const tr = document.createElement("tr");
    tr.innerHTML =
      "<td>📁 " + esc(m.name) + "</td>" +
      "<td><code>" + esc(m.path) + "</code></td>" +
      '<td class="acts"><button class="btn small danger" data-rm="' + esc(m.name) + '">Убрать</button></td>';
    tb.appendChild(tr);
  }
  tb.querySelectorAll("[data-rm]").forEach(b => b.addEventListener("click", async () => {
    if (!confirm("Убрать папку «" + b.dataset.rm + "» из шаринга? Файлы на диске не удалятся.")) return;
    try {
      await api("/api/mounts?name=" + encodeURIComponent(b.dataset.rm), { method: "DELETE" });
      loadMounts();
      toast("Папка убрана");
    } catch (err) { toast(err.message, true); }
  }));
}

$("#m-browse").addEventListener("click", () => openBrowse());
$("#m-add").addEventListener("click", async () => {
  const path = $("#m-path").value.trim();
  if (!path) { toast("Сначала выберите папку кнопкой «Обзор»", true); return; }
  try {
    await post("/api/mounts", { name: $("#m-name").value.trim(), path });
    $("#m-path").value = "";
    $("#m-name").value = "";
    toast("Папка добавлена — появится в списке «Файлы»");
    loadMounts();
  } catch (err) { toast(err.message, true); }
});

// ---------- BROWSE MODAL ----------

let brPath = "";

function openBrowse() {
  brPath = "";
  $("#browse-modal").classList.remove("hidden");
  loadBrowse();
}

$("#br-close").addEventListener("click", () => $("#browse-modal").classList.add("hidden"));
$("#br-select").addEventListener("click", () => {
  if (!brPath) return;
  $("#m-path").value = brPath;
  if (!$("#m-name").value.trim()) {
    const parts = brPath.replace(/\\+$/, "").split("\\");
    $("#m-name").value = parts[parts.length - 1] || "";
  }
  $("#browse-modal").classList.add("hidden");
});

async function loadBrowse(path) {
  if (path !== undefined) brPath = path;
  let d;
  try {
    d = await api("/api/browse?path=" + encodeURIComponent(brPath));
  } catch (err) { toast(err.message, true); return; }

  const drivesEl = $("#br-drives");
  drivesEl.innerHTML = "";
  const tb = $("#br-body");
  tb.innerHTML = "";
  $("#br-empty").classList.add("hidden");
  $("#br-select").disabled = !brPath;

  if (!brPath) {
    $("#br-crumb").textContent = "Диски сервера";
    for (const dv of d.drives || []) {
      const b = document.createElement("button");
      b.className = "drive-btn";
      b.textContent = dv;
      b.addEventListener("click", () => loadBrowse(dv));
      drivesEl.appendChild(b);
    }
    if (!(d.drives || []).length) {
      $("#br-empty").textContent = "Диски не найдены";
      $("#br-empty").classList.remove("hidden");
    }
    return;
  }

  $("#br-crumb").innerHTML = crumbDriveHtml(brPath);
  document.querySelectorAll("#br-crumb a").forEach(a => a.addEventListener("click", e => {
    e.preventDefault();
    loadBrowse(a.dataset.p);
  }));

  if (!d.entries.length) {
    $("#br-empty").textContent = "Нет подпапок";
    $("#br-empty").classList.remove("hidden");
  }
  for (const en of d.entries) {
    const tr = document.createElement("tr");
    tr.innerHTML =
      '<td><span class="icon">📁</span><a class="name-link" data-p="' + esc(en.path) + '">' + esc(en.name) + "</a></td>" +
      "<td>" + esc(en.modtime) + "</td>";
    tb.appendChild(tr);
  }
  tb.querySelectorAll("[data-p]").forEach(a =>
    a.addEventListener("click", () => loadBrowse(a.dataset.p)));
}

function crumbDriveHtml(p) {
  const norm = p.replace(/\//g, "\\");
  let volume = "";
  if (/^[A-Za-z]:\\/.test(norm)) volume = norm.slice(0, 3).toUpperCase();
  let html = '<a href="#" data-p="">💽 Диски</a>';
  if (volume) html += ' / <a href="#" data-p="' + esc(volume) + '">' + esc(volume) + "</a>";
  const rest = volume ? norm.slice(3) : norm;
  const parts = rest.split("\\").filter(Boolean);
  let acc = volume;
  for (const seg of parts) {
    acc = acc ? acc + "\\" + seg : seg;
    html += ' / <a href="#" data-p="' + esc(acc) + '">' + esc(seg) + "</a>";
  }
  return html;
}

$("#set-save").addEventListener("click", async () => {
  try {
    await api("/api/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        web_port: +$("#set-web").value,
        sftp_port: +$("#set-sftp").value,
        bind: $("#set-bind").value.trim(),
        root_dir: $("#set-root").value.trim(),
        https: $("#set-https").checked,
        zip_max_bytes: +$("#set-zip").value,
        external_ip: $("#set-ip").value.trim(),
        session_ttl_minutes: +$("#set-ttl").value,
      }),
    });
    toast("Сохранено. Если менялся порт — обновите страницу.");
  } catch (err) { toast(err.message, true); }
});

// ---------- NET ----------

async function loadNet() {
  let n;
  try { n = await api("/api/netinfo"); } catch (err) { toast(err.message, true); return; }
  $("#net-ext").textContent = n.external_ip || "не определён (задайте в настройках)";
  $("#net-lan").textContent = (n.local_ips || []).join(", ") || "—";
  $("#net-web").textContent = n.web_port;
  $("#net-sftp").textContent = n.sftp_port;
  $("#net-instr").textContent = n.instructions;
  const scheme = n.https ? "https" : "http";
  $("#net-url").textContent = scheme + "://" + (n.external_ip || "ВАШ_ИП") + ":" + n.web_port;
}

// ---------- PASSWORD ----------

$("#pw-save").addEventListener("click", async () => {
  if ($("#pw-new").value !== $("#pw-new2").value) {
    toast("Пароли не совпадают", true);
    return;
  }
  try {
    await post("/api/password", {
      old_password: $("#pw-old").value,
      new_password: $("#pw-new").value,
    });
    $("#pw-old").value = ""; $("#pw-new").value = ""; $("#pw-new2").value = "";
    toast("Пароль изменён");
  } catch (err) { toast(err.message, true); }
});

// ---------- INIT ----------

(async () => {
  try {
    me = await api("/api/me");
    showApp();
  } catch (e) {
    showLogin();
  }
})();
