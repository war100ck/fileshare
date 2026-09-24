(function () {
  let overlay, titleEl, statusEl, fillEl, detailEl, failedEl;
  let btnCancel, btnClose, btnRetry;
  let ticker = null;

  function human(n) {
    if (n == null) return "0 Б";
    if (n < 1024) return n + " Б";
    const u = ["КБ", "МБ", "ГБ", "ТБ"];
    let i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
    return n.toFixed(1) + " " + u[i];
  }

  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  function cancelErr() { const e = new Error("Отменено"); e.cancelledFlag = true; return e; }

  function ensureDom() {
    if (overlay) return;
    overlay = document.createElement("div");
    overlay.className = "modal-overlay hidden";
    overlay.innerHTML =
      '<div class="modal">' +
      '<h3 id="tdl-title" style="margin-top:0">Скачивание папки</h3>' +
      '<div class="hint" id="tdl-status">Подготовка…</div>' +
      '<div class="dl-bar"><div class="dl-fill" id="tdl-fill"></div></div>' +
      '<div class="hint" id="tdl-detail"></div>' +
      '<div class="dl-failed hidden" id="tdl-failed"></div>' +
      '<div class="toolbar" style="margin-top:14px">' +
      '<button class="btn primary hidden" id="tdl-retry">Повторить ошибки</button>' +
      '<div class="spacer"></div>' +
      '<button class="btn" id="tdl-cancel">Отмена</button>' +
      '<button class="btn hidden" id="tdl-close">Закрыть</button>' +
      "</div></div>";
    document.body.appendChild(overlay);
    titleEl = overlay.querySelector("#tdl-title");
    statusEl = overlay.querySelector("#tdl-status");
    fillEl = overlay.querySelector("#tdl-fill");
    detailEl = overlay.querySelector("#tdl-detail");
    failedEl = overlay.querySelector("#tdl-failed");
    btnCancel = overlay.querySelector("#tdl-cancel");
    btnClose = overlay.querySelector("#tdl-close");
    btnRetry = overlay.querySelector("#tdl-retry");
    btnClose.addEventListener("click", () => overlay.classList.add("hidden"));
  }

  function setFill(done, total) {
    const pct = total > 0 ? Math.min(100, (done / total) * 100) : 0;
    fillEl.style.width = pct.toFixed(1) + "%";
  }

  function stopTicker() {
    if (ticker) { clearInterval(ticker); ticker = null; }
  }

  async function getManifest(url) {
    const r = await fetch(url, { credentials: "same-origin" });
    if (!r.ok) {
      let msg = "HTTP " + r.status;
      try { const j = await r.json(); if (j.error) msg = j.error; } catch (e) {}
      throw new Error(msg);
    }
    return r.json();
  }

  window.torrentDownload = async function (opts) {
    ensureDom();
    stopTicker();
    const st = { cancelled: false };
    overlay.classList.remove("hidden");
    titleEl.textContent = "Скачивание: " + (opts.label || "папка");
    statusEl.textContent = "Получение списка файлов…";
    fillEl.style.width = "0%";
    detailEl.textContent = "";
    failedEl.classList.add("hidden");
    failedEl.textContent = "";
    btnRetry.classList.add("hidden");
    btnClose.classList.add("hidden");
    btnCancel.classList.remove("hidden");
    btnCancel.onclick = () => { st.cancelled = true; statusEl.textContent = "Отмена…"; };

    let m;
    try {
      m = await getManifest(opts.manifestUrl);
    } catch (e) {
      statusEl.textContent = "Ошибка: " + e.message;
      btnCancel.classList.add("hidden");
      btnClose.classList.remove("hidden");
      return;
    }
    if (!m.files || !m.files.length) {
      statusEl.textContent = "Нет файлов для скачивания";
      btnCancel.classList.add("hidden");
      btnClose.classList.remove("hidden");
      return;
    }

    const total = m.total_size || 0;
    let done = 0, filesDone = 0, filesTotal = m.files.length;
    let lastTickDone = 0, lastTickTime = Date.now();

    function statusLine(base) {
      const now = Date.now();
      const dt = (now - lastTickTime) / 1000;
      let speed = 0;
      if (dt > 0.4) {
        speed = (done - lastTickDone) / dt;
        lastTickDone = done;
        lastTickTime = now;
      }
      const pct = total > 0 ? ((done / total) * 100).toFixed(1) : "0.0";
      detailEl.textContent =
        human(done) + " / " + human(total) + " · " + pct + "% · файлы " +
        filesDone + "/" + filesTotal +
        (speed > 0 ? " · " + human(Math.round(speed)) + "/с" : "");
      setFill(done, total);
      if (base) statusEl.textContent = base;
    }

    const secure = window.isSecureContext && "showDirectoryPicker" in window;
    if (!secure) {
      await fallbackDownload(m, opts, st, { total, statusLine: s => statusLine(s) });
      return;
    }

    let root;
    try {
      root = await window.showDirectoryPicker({ mode: "readwrite" });
    } catch (e) {
      if (e && e.name === "AbortError") { overlay.classList.add("hidden"); return; }
      root = null;
    }
    if (!root) {
      await fallbackDownload(m, opts, st, { total, statusLine: s => statusLine(s) });
      return;
    }

    btnCancel.classList.remove("hidden");
    const progress = (delta) => {
      done += delta;
      if (delta > 0) statusLine();
    };

    async function writeStream(dh, fname, url, offset) {
      for (let guard = 0; guard < 500; guard++) {
        if (st.cancelled) throw cancelErr();
        const headers = offset > 0 ? { Range: "bytes=" + offset + "-" } : {};
        const resp = await fetch(url, { headers, credentials: "same-origin" });
        if (resp.status === 401) throw new Error("нужен пароль ссылки");
        if (resp.status === 416) return;
        if (!(resp.ok || resp.status === 206)) throw new Error("HTTP " + resp.status);
        let start = offset;
        if (offset > 0 && resp.status !== 206) {
          start = 0;
          progress(-offset);
        }
        const fh = await dh.getFileHandle(fname, { create: true });
        const w = await fh.createWritable({ keepExistingData: start > 0 });
        if (start > 0) await w.seek(start);
        let written = start;
        let commitRestart = false;
        try {
          const reader = resp.body.getReader();
          while (true) {
            const r = await reader.read();
            if (r.done) break;
            if (st.cancelled) {
              try { await w.abort(); } catch (e) {}
              throw cancelErr();
            }
            await w.write(r.value);
            written += r.value.length;
            progress(r.value.length);
            if (written - start >= 256 * 1024 * 1024) {
              await w.close();
              commitRestart = true;
              break;
            }
          }
          if (!commitRestart) {
            await w.close();
            return;
          }
          offset = written;
          continue;
        } catch (e) {
          if (e && e.cancelledFlag) throw e;
          try { await w.close(); } catch (e2) {}
          const fl = await (await dh.getFileHandle(fname)).getFile();
          if (fl.size <= offset) throw e;
          offset = fl.size;
          continue;
        }
      }
      throw new Error("слишком много повторов");
    }

    async function dlOne(f) {
      const rel = f.path;
      const parts = rel.split("/").filter(Boolean);
      const fname = parts.pop();
      let dh = root;
      for (const p of parts) dh = await dh.getDirectoryHandle(p, { create: true });

      let offset = 0;
      try {
        const fl = await (await dh.getFileHandle(fname)).getFile();
        if (f.size > 0 && fl.size === f.size) {
          progress(f.size);
          filesDone++;
          return;
        }
        if (fl.size < f.size) {
          offset = fl.size;
          progress(offset);
        }
      } catch (e) {}

      if (f.size > 0 && offset === f.size) {
        filesDone++;
        return;
      }

      let lastErr = null;
      for (let att = 1; att <= 4; att++) {
        if (st.cancelled) throw cancelErr();
        try {
          await writeStream(dh, fname, opts.fileUrl(rel), offset);
          const fl = await (await dh.getFileHandle(fname)).getFile();
          if (f.size > 0 && fl.size !== f.size) {
            throw new Error("размер " + fl.size + " из " + f.size);
          }
          filesDone++;
          statusLine();
          return;
        } catch (e) {
          if (e && e.cancelledFlag) throw e;
          lastErr = e;
          try {
            const fl = await (await dh.getFileHandle(fname)).getFile();
            if (fl.size > offset && fl.size < f.size) offset = fl.size;
          } catch (e2) {}
          await sleep(400 * att);
        }
      }
      throw lastErr || new Error("ошибка");
    }

    async function runFiles(files) {
      const failed = [];
      const q = files.slice();
      const workers = Math.min(4, q.length);
      async function worker() {
        while (q.length) {
          if (st.cancelled) return;
          const f = q.shift();
          try {
            await dlOne(f);
          } catch (e) {
            if (e && e.cancelledFlag) return;
            failed.push({ path: f.path, size: f.size || 0, err: String(e && e.message || e) });
          }
        }
      }
      const arr = [];
      for (let i = 0; i < workers; i++) arr.push(worker());
      await Promise.all(arr);
      return failed;
    }

    let current = m.files;
    let failed = [];
    lastTickDone = 0;
    lastTickTime = Date.now();
    statusLine("Скачивание…");
    ticker = setInterval(() => statusLine(), 500);

    async function cycle(files) {
      statusLine("Скачивание…");
      failed = await runFiles(files);
      if (st.cancelled) {
        stopTicker();
        statusEl.textContent = "Отменено";
        btnCancel.classList.add("hidden");
        btnClose.classList.remove("hidden");
        return;
      }
      stopTicker();
      setFill(done, total);
      if (!failed.length) {
        statusEl.textContent = "Готово: " + filesTotal + " файл(ов)";
        detailEl.textContent = human(done) + " / " + human(total);
        btnCancel.classList.add("hidden");
        btnClose.classList.remove("hidden");
        return;
      }
      statusEl.textContent = "Скачано с ошибками: " + failed.length + " из " + filesTotal;
      failedEl.textContent = failed.slice(0, 20)
        .map(x => x.path + " — " + x.err)
        .join("\n") + (failed.length > 20 ? "\n…" : "");
      failedEl.classList.remove("hidden");
      btnCancel.classList.add("hidden");
      btnClose.classList.remove("hidden");
      btnRetry.classList.remove("hidden");
      btnRetry.onclick = () => {
        btnRetry.classList.add("hidden");
        btnClose.classList.add("hidden");
        btnCancel.classList.remove("hidden");
        const retryFiles = failed.map(x => ({ path: x.path, size: x.size }));
        let fbytes = 0;
        for (const x of failed) fbytes += x.size;
        done = Math.max(0, total - fbytes);
        filesDone = Math.max(0, filesTotal - failed.length);
        lastTickDone = done;
        lastTickTime = Date.now();
        st.cancelled = false;
        ticker = setInterval(() => statusLine(), 500);
        cycle(retryFiles);
      };
    }

    await cycle(current);
  };

  async function fallbackDownload(m, opts, st, ui) {
    statusEl.textContent = "";
    ui.statusLine(
      "Браузер/HTTP не даёт выбирать папку — файлы пойдут в «Загрузки» по одному. " +
      "Для папок со структурой: Chrome/Edge + HTTPS, либо загрузчик fileshare.exe."
    );
    let i = 0;
    for (const f of m.files) {
      if (st.cancelled) break;
      const a = document.createElement("a");
      a.href = opts.fileUrl(f.path);
      a.download = "";
      a.rel = "noopener";
      document.body.appendChild(a);
      a.click();
      a.remove();
      i++;
      ui.statusLine(
        "Файл " + i + "/" + m.files.length + " — разрешите множественные загрузки, если браузер спросит"
      );
      await sleep(500);
    }
    stopTicker();
    btnCancel.classList.add("hidden");
    btnClose.classList.remove("hidden");
    if (st.cancelled) statusEl.textContent = "Отменено";
    else statusEl.textContent = "Запущено файлов: " + i + " (смотрите папку «Загрузки»)";
  }
})();
