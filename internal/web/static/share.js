const $ = s => document.querySelector(s);

const token = location.pathname.replace(/^\/s\//, "").split("/")[0];
const base = "/s/" + token;
let curPath = "";
let authed = false;

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

async function tryList(path) {
  const r = await fetch(base + "/list?path=" + encodeURIComponent(path || ""));
  const data = await r.json().catch(() => ({}));
  return { r, data };
}

function showGate() {
  $("#pwd-gate").classList.remove("hidden");
  $("#content").classList.add("hidden");
  $("#pwd-input").focus();
}

function showContent() {
  $("#pwd-gate").classList.add("hidden");
  $("#content").classList.remove("hidden");
}

$("#pwd-btn").addEventListener("click", doAuth);
$("#pwd-input").addEventListener("keydown", e => { if (e.key === "Enter") doAuth(); });

async function doAuth() {
  $("#pwd-err").textContent = "";
  try {
    const r = await fetch(base + "/auth", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: $("#pwd-input").value }),
    });
    const d = await r.json();
    if (!r.ok) { $("#pwd-err").textContent = d.error || "Ошибка"; return; }
    authed = true;
    showContent();
    load();
  } catch (e) {
    $("#pwd-err").textContent = "Ошибка сети";
  }
}

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

async function load(path) {
  if (path !== undefined) curPath = path;
  const { r, data } = await tryList(curPath);
  if (r.status === 401) { showGate(); return; }
  if (!r.ok) { toastErr(data.error || "Ошибка"); return; }
  authed = true;
  showContent();
  curPath = data.path;
  $("#sh-title").textContent = data.name || "Скачивание";

  $("#crumb").innerHTML = crumbHtml(curPath);
  document.querySelectorAll("#crumb a").forEach(a => a.addEventListener("click", e => {
    e.preventDefault();
    load(a.dataset.p);
  }));

  const tb = $("#body");
  tb.innerHTML = "";
  $("#empty").classList.toggle("hidden", data.entries.length > 0);

  let total = 0;
  for (const en of data.entries) {
    total += en.is_dir ? 0 : en.size;
    const tr = document.createElement("tr");
    const icon = en.is_dir ? "📁" : "📄";
    const fileUrl = base + "/file/" + en.path.split("/").map(encodeURIComponent).join("/");
    tr.innerHTML =
      "<td><span class='icon'>" + icon + "</span>" +
      (en.is_dir
        ? '<a class="name-link" data-open="' + esc(en.path) + '">' + esc(en.name) + "</a>"
        : esc(en.name)) +
      "</td>" +
      "<td class='num'>" + (en.is_dir ? "—" : human(en.size)) + "</td>" +
      "<td>" + esc(en.modtime) + "</td>" +
      "<td class='acts'>" +
      (en.is_dir
        ? '<button class="btn small" data-dl="' + esc(en.path) + '">Скачать</button>'
        : '<a class="btn small" href="' + fileUrl + '">Скачать</a>') +
      "</td>";
    tb.appendChild(tr);
  }
  tb.querySelectorAll("[data-open]").forEach(a =>
    a.addEventListener("click", () => load(a.dataset.open)));
  tb.querySelectorAll("[data-dl]").forEach(b =>
    b.addEventListener("click", () => downloadShareFolder(b.dataset.dl)));

  $("#btn-manifest").href = base + "/manifest.json";
  try {
    const b = await fetch("/api/base").then(r => r.json()).catch(() => ({}));
    const o = b.public_base || location.origin;
    $("#dl-cmd").textContent = "fileshare download " + o + base + " -o Downloads";
  } catch (e) {
    $("#dl-cmd").textContent = "fileshare download " + location.origin + base + " -o Downloads";
  }
}

function parentOf(p) {
  if (!p) return "";
  const i = p.lastIndexOf("/");
  return i < 0 ? "" : p.slice(0, i);
}

$("#btn-up").addEventListener("click", () => load(parentOf(curPath)));

function downloadShareFolder(rel) {
  const label = rel || "вся папка";
  torrentDownload({
    label,
    manifestUrl: base + "/manifest.json?hash=0" + (rel ? "&path=" + encodeURIComponent(rel) : ""),
    fileUrl: r => {
      const full = rel ? rel + "/" + r : r;
      return base + "/file/" + full.split("/").map(encodeURIComponent).join("/");
    },
  });
}

$("#btn-dl").addEventListener("click", () => downloadShareFolder(curPath));

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

$("#copy-cmd").addEventListener("click", async () => {
  const text = $("#dl-cmd").textContent;
  const ok = await copyText(text);
  $("#copy-cmd").textContent = ok ? "Скопировано" : "Скопируйте вручную";
  if (!ok) alert(text);
  setTimeout(() => $("#copy-cmd").textContent = "Копировать команду", 1500);
});

function toastErr(msg) {
  alert(msg);
}

load();
