"use strict";
// Recomp launcher interface. Talks to the Go side through Wails (window.go.main.App.* and runtime events).
// Rule: nothing that comes from the server or from other players is inserted as HTML -- only as text.

const api = () => window.go.main.App;
const rt = () => window.runtime;
const $ = (id) => document.getElementById(id);

const st = { state: null, me: "", players: [], rooms: [], friends: [], blocked: [], room: null, invites: new Map(), connected: false };

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined && text !== null) e.textContent = text;
  return e;
}
function button(cls, text, fn) {
  const b = el("button", cls, text);
  b.type = "button";
  b.addEventListener("click", fn);
  return b;
}
const same = (a, b) => (a || "").toLowerCase() === (b || "").toLowerCase();

// ---------------- tabs, window ----------------
function show(view) {
  document.querySelectorAll(".tab").forEach((t) => t.classList.toggle("active", t.dataset.view === view));
  for (const v of ["play", "online", "settings"]) {
    const s = $("v-" + v), was = !s.classList.contains("hidden");
    s.classList.toggle("hidden", v !== view);
    if (v === view && !was) { s.classList.remove("enter"); void s.offsetWidth; s.classList.add("enter"); }
  }
  if (view === "settings") voiceSettingsShown();
}

// rows that were not there in the previous render slide in; the rest are redrawn still
const seen = new Map(); // list -> keys drawn last time
function fresh(list, key, row) {
  const now = seen.get(list + "+") || new Set();
  now.add(key);
  seen.set(list + "+", now);
  const before = seen.get(list);
  if (before && !before.has(key)) row.classList.add("fresh");
  row.dataset.k = list + ":" + key;
  return row;
}

// a redraw replaces the rows: whatever had the focus (controller) gets it back on the same row
function keepFocus(draw) {
  const a = document.activeElement, row = a && a.closest("[data-k]");
  let k = null, i = -1;
  if (row) { k = row.dataset.k; i = [...row.querySelectorAll("button")].indexOf(a); }
  draw();
  if (!k) return;
  const again = [...document.querySelectorAll("[data-k]")].find((e) => e.dataset.k === k);
  const target = again && (again.querySelectorAll("button")[i] || again);
  if (target) { if (target === again) target.tabIndex = -1; target.focus({ preventScroll: true }); }
}
function drawn(list) { seen.set(list, seen.get(list + "+") || new Set()); seen.delete(list + "+"); }
document.querySelectorAll(".tab").forEach((t) => t.addEventListener("click", () => show(t.dataset.view)));
$("userchip").addEventListener("click", () => show("online"));
$("w-min").addEventListener("click", () => rt().WindowMinimise());
$("w-close").addEventListener("click", () => api().Quit());
$("l-github").addEventListener("click", (e) => { e.preventDefault(); api().OpenLink("github"); });
$("btn-back-game").addEventListener("click", () => api().HideOverlay());
// leaving an online match closes the game: asked twice, so a stray press of A on the pad cannot end it
let leaveArmed = 0;
$("btn-leave-match").addEventListener("click", () => {
  const b = $("btn-leave-match");
  if (Date.now() - leaveArmed > 4000) {
    leaveArmed = Date.now();
    b.textContent = "¿Seguro? Pulsa otra vez para salir";
    setTimeout(() => { if (Date.now() - leaveArmed >= 4000) b.textContent = "Salir de la partida online"; }, 4100);
    return;
  }
  leaveArmed = 0;
  b.textContent = "Salir de la partida online";
  api().LeaveMatch();
});

// ---------------- avatars ----------------
function hue(n) { let h = 7; for (const c of n || "") h = (h * 31 + c.charCodeAt(0)) % 360; return h; }
function pic(name, status, cls) {
  const p = el("span", "pic " + (cls || "") + " " + (status === "waiting" ? "waiting" : status === "playing" ? "playing" : status ? "online" : ""));
  p.textContent = (name || "?").slice(0, 1).toUpperCase();
  p.style.background = name ? "hsl(" + hue(name) + " 32% 34%)" : "";
  return p;
}

// ---------------- notifications ----------------
function note(title, text, kind, actions, seconds, who) {
  const n = el("div", "note " + (kind || ""));
  if (who) n.appendChild(pic(who, "online"));
  const body = el("div", "n-body");
  body.appendChild(el("div", "n-title", title));
  if (text) body.appendChild(el("div", "n-text", text));
  n.appendChild(body);
  let gone = false;
  const close = () => { if (gone) return; gone = true; n.classList.add("out"); setTimeout(() => n.remove(), 180); };
  if (actions && actions.length) {
    const row = el("div", "btnrow");
    for (const a of actions) row.appendChild(button("btn " + (a.cls || ""), a.label, () => { close(); a.fn(); }));
    body.appendChild(row);
  }
  $("toasts").appendChild(n);
  setTimeout(close, (seconds || 6) * 1000);
  return n;
}
const fail = (msg) => { if (msg) note("Algo falló", msg, "bad"); return !msg; };

// ---------------- the games (sidebar) ----------------
function renderGames() {
  const s = st.state, box = $("games");
  box.textContent = "";
  for (const g of s.games || []) {
    const r = el("div", "game-row" + (g.selected ? " on" : "") + (g.supported ? "" : " off"));
    r.appendChild(el("span", "g-icon " + g.banner));
    r.appendChild(el("span", null, g.short));
    if (!g.supported) r.appendChild(el("em", null, "pronto"));
    else if (!g.installed) r.appendChild(el("em", null, "instalar"));
    if (g.supported && !g.selected) {
      r.tabIndex = 0;
      r.classList.add("pick");
      const pick = async () => { if (fail(await api().SelectGame(g.id))) { refresh(); } };
      r.addEventListener("click", pick);
      r.addEventListener("keydown", (e) => { if (e.key === "Enter") pick(); });
    }
    box.appendChild(r);
  }
}

// ---------------- library page ----------------
// the quality presets, the game's own switches and a few texts differ per game and system
function renderGame(s) {
  const key = s.game + "/" + s.platform;
  const sel = $("s-quality");
  if (s.qualities && sel.dataset.key !== key) {
    sel.dataset.key = key;
    sel.textContent = "";
    s.qualities.forEach((q, i) => { const o = el("option", null, q.name); o.value = String(i); sel.appendChild(o); });
  }
  const banner = $("banner");
  if (banner.dataset.game !== s.game) {
    banner.dataset.game = s.game;
    banner.className = "banner " + s.banner;
    $("banner-title").textContent = s.title;
    $("banner-title").className = s.banner === "dbz" ? "logo-in-art" : "";
    // the game's own switches, before "Pantalla completa"
    document.querySelectorAll(".opt.game-opt").forEach((o) => o.remove());
    for (const o of s.options || []) {
      const row = el("div", "opt game-opt");
      const lab = el("label", null, o.label);
      lab.htmlFor = "go-" + o.key;
      const ctl = el("div", "ctl");
      const cb = el("input", "sw");
      cb.type = "checkbox";
      cb.id = "go-" + o.key;
      cb.dataset.key = o.key;
      cb.addEventListener("change", saveSettings);
      ctl.appendChild(cb);
      ctl.appendChild(el("small", null, o.hint));
      row.appendChild(lab);
      row.appendChild(ctl);
      $("opt-full").before(row);
    }
    $("controls").innerHTML = s.controls; // the launcher's own text (profiles.go), never anything from the network
    $("p-game").textContent = s.short;
  }
  $("banner-sub").textContent = s.sub + " para " + s.platform;
  $("full-hint").textContent = s.fullscreenKey + " cambia durante la partida.";
  const mac = s.platform === "macOS";
  $("dlss-card").classList.toggle("hidden", !mac && !s.hasDLSS);
  if (mac) {
    $("dlss-label").textContent = "Partidas y registros";
    $("dlss-hint").textContent = s.dataFolder;
  }
  return mac;
}

function renderPlay() {
  const s = st.state;
  $("ver").textContent = "Launcher " + s.version;
  const installing = st.installing === s.game;
  $("btn-play").classList.toggle("hidden", !s.installed);
  $("btn-install").classList.toggle("hidden", s.installed);
  $("btn-install").disabled = installing;
  $("btn-install").textContent = installing ? "Instalando…" : "Instalar";
  $("btn-play").disabled = !s.ready || s.playing;
  $("btn-play").textContent = s.playing ? "Jugando" : "Jugar";
  $("iso-line").textContent = !s.installed ? (installing ? st.installText : "Se descarga ya compilado, listo para jugar con tu ISO.")
    : s.ready ? s.iso : s.iso ? "No encuentro la ISO que elegiste." : s.isoMissing;
  $("btn-iso").classList.toggle("hidden", !s.installed);
  $("btn-iso").textContent = s.ready ? "Cambiar ISO" : "Seleccionar ISO";
  renderGames();
  const mac = renderGame(s);
  $("s-quality").value = String(s.settings.quality);
  $("q-hint").textContent = (s.qualities && s.qualities[s.settings.quality] || {}).hint || "";
  document.querySelectorAll(".opt.game-opt input").forEach((cb) => { cb.checked = !!(s.settings.opts || {})[cb.dataset.key]; });
  $("s-full").checked = s.settings.fullscreen;
  $("s-delay").value = String(s.settings.netDelay);
  const d = s.dlss;
  $("dlss-line").textContent = mac ? "Memoria, ajustes y logs/juego.log." : "Versión " + d.version + (d.own ? " (la tuya)" : "") + (d.dlss5 ? " · DLSS 5 disponible" : "");
  $("s-server").value = s.server;
  $("acc-line").textContent = s.loggedIn ? "Conectado como " + s.user : "No has entrado";
  $("btn-logout").classList.toggle("hidden", !s.loggedIn);
  $("build-line").textContent = "Versión del juego " + s.build + ". Para jugar en línea, los dos tenéis que tener la misma.";
}

async function refresh() {
  st.state = await api().State();
  if (st.state.user) st.me = st.state.user;
  renderPlay();
  renderMe();
  renderOnline();
}

async function saveSettings() {
  const s = st.state.settings;
  s.quality = parseInt($("s-quality").value, 10);
  s.fullscreen = $("s-full").checked;
  s.opts = {};
  document.querySelectorAll(".opt.game-opt input").forEach((cb) => { s.opts[cb.dataset.key] = cb.checked; });
  s.netDelay = parseInt($("s-delay").value, 10);
  if (fail(await api().SaveSettings(s))) refresh();
}
["s-quality", "s-full", "s-delay"].forEach((id) => $(id).addEventListener("change", saveSettings));

// ---------------- adding and updating games ----------------
async function openAdd() {
  $("add-err").textContent = "";
  const cat = $("cat");
  cat.textContent = "";
  for (const c of (await api().Catalog()) || []) {
    const r = el("div", "r");
    const t = el("div", "r-txt");
    t.appendChild(el("span", "r-name", c.title));
    t.appendChild(el("span", "r-sub", !c.supported ? "Todavía no para este sistema" : c.inApp ? "Incluido en Recomp " + c.version + ": se actualiza con Recomp"
      : c.installed ? "Instalado" + (c.version ? " · versión " + c.version : "") : c.repo));
    r.appendChild(t);
    if (c.supported) {
      const act = el("div", "act keep");
      act.appendChild(button("btn" + (c.installed ? "" : " green"), c.installed ? "Buscar actualización" : "Instalar", () => addGame(c.id)));
      r.appendChild(act);
    }
    cat.appendChild(r);
  }
  $("m-add").classList.remove("hidden");
}
async function addGame(idOrRepo) {
  const e = await api().AddGame(idOrRepo);
  if (e) { $("add-err").textContent = e; return; }
  $("m-add").classList.add("hidden");
}
$("btn-add-game").addEventListener("click", openAdd);
$("btn-add-close").addEventListener("click", () => $("m-add").classList.add("hidden"));
$("btn-add-url").addEventListener("click", () => addGame($("add-url").value));
$("add-url").addEventListener("keydown", (e) => { if (e.key === "Enter") addGame($("add-url").value); });
$("btn-install").addEventListener("click", () => { if (st.state) addGame(st.state.game); });
const mb = (n) => (n / 1048576).toFixed(0) + " MB";
function onInstall(ev) {
  const title = ((st.state && st.state.games) || []).find((g) => g.id === ev.game);
  const name = title ? title.short : ev.game;
  switch (ev.phase) {
    case "download":
      st.installing = ev.game;
      st.installText = "Descargando " + name + ": " + mb(ev.done) + (ev.total > 0 ? " de " + mb(ev.total) : "");
      break;
    case "unpack":
      st.installText = "Instalando " + name + "…";
      break;
    case "done":
      st.installing = null;
      note(name + " está listo", "Versión " + ev.version + ". Elige tu ISO y a jugar.", "good", null, 8);
      break;
    case "current":
      note(name + " está al día", "Viene incluido en Recomp " + ev.version + " y se actualiza junto con Recomp.", "good", null, 8);
      break;
    case "error":
      st.installing = null;
      note("No se pudo instalar " + name, ev.error, "bad", null, 12);
      break;
  }
  if (st.state) renderPlay();
  if (ev.phase === "done" || ev.phase === "error") refresh();
}
$("btn-iso").addEventListener("click", async () => { const e = await api().ChooseISO(); if (e) fail(e); refresh(); });
$("btn-play").addEventListener("click", async () => { fail(await api().Play()); refresh(); });
$("btn-dlss").addEventListener("click", () => api().OpenDLSSFolder());
const dc = $("dlss-card");
["dragenter", "dragover"].forEach((ev) => document.addEventListener(ev, (e) => { e.preventDefault(); if (!st.state || st.state.platform !== "macOS") dc.classList.add("drop"); }));
["dragleave", "drop"].forEach((ev) => document.addEventListener(ev, (e) => { e.preventDefault(); dc.classList.remove("drop"); }));

// ---------------- the account in the title bar ----------------
const statusWords = { lobby: "En línea", waiting: "Buscando partida", room: "En una sala", playing: "Jugando" };
function myStatus() { const me = st.players.find((p) => same(p.user, st.me)); return me ? me.status : ""; }

function renderMe() {
  const s = st.state;
  const slot = $("u-avatar");
  const p = pic(s.loggedIn ? s.user : "", s.loggedIn && st.connected ? myStatus() || "lobby" : "");
  slot.className = p.className; slot.textContent = p.textContent; slot.style.background = p.style.background;
  $("u-name").textContent = s.loggedIn ? s.user : "Invitado";
  const us = $("u-status");
  us.textContent = !s.loggedIn ? "sin conectar" : st.connected ? (statusWords[myStatus()] || "En línea").toLowerCase() : "reconectando…";
  us.classList.toggle("on", s.loggedIn && st.connected);
}

// ---------------- online ----------------
function renderFriends() {
  const box = $("friends");
  box.replaceChildren();
  const logged = st.state && st.state.loggedIn;
  const list = st.players.filter((p) => p.friend && !same(p.user, st.me));
  // friends who are not here: still listed, greyed out, after the ones online
  const away = st.friends.filter((f) => !list.some((p) => same(p.user, f))).sort((a, b) => a.localeCompare(b));
  $("f-count").textContent = logged && list.length ? list.length + " en línea" : "";
  if (!logged) { box.appendChild(el("div", "side-empty", "Entra en la pestaña En línea para ver a tus amigos.")); return; }
  if (!list.length && !away.length) { box.appendChild(el("div", "side-empty", "Busca a alguien por su nombre o marca con la estrella a la gente con la que juegues, y aparecerá aquí.")); return; }
  for (const p of list) {
    const f = el("div", "friend");
    f.appendChild(pic(p.user, p.status));
    const t = el("div", "f-txt");
    t.appendChild(el("span", "f-name", p.user));
    t.appendChild(el("span", "f-st " + p.status, statusWords[p.status]));
    f.appendChild(t);
    f.title = "Clic para invitar a " + p.user;
    f.addEventListener("click", () => invite(p));
    box.appendChild(fresh("friends", p.user.toLowerCase(), f));
  }
  for (const name of away) {
    const f = el("div", "friend off");
    f.appendChild(pic(name, ""));
    const t = el("div", "f-txt");
    t.appendChild(el("span", "f-name", name));
    t.appendChild(el("span", "f-st off", "Desconectado"));
    f.appendChild(t);
    box.appendChild(fresh("friends", name.toLowerCase(), f));
  }
  drawn("friends");
}

// ---------------- search by nickname ----------------
// The box filters the people online here at once; from 2 letters it also asks the server for every account with
// that in its name (offline, or in another game), to add them as friends.
const search = { q: "", users: [], timer: 0, seq: 0 };
$("find").addEventListener("input", (e) => {
  search.q = e.target.value.trim();
  clearTimeout(search.timer);
  if (search.q.length < 2) { search.users = []; renderOnline(); return; }
  renderOnline();
  const q = search.q, seq = ++search.seq;
  search.timer = setTimeout(async () => {
    const r = await api().Search(q);
    if (seq !== search.seq) return; // a newer search is on its way
    search.users = r.users || [];
    if (r.error) note("No se pudo buscar", r.error, "bad");
    renderOnline();
  }, 280);
});
$("find").addEventListener("keydown", (e) => { if (e.key === "Escape") { e.target.value = ""; e.target.dispatchEvent(new Event("input")); } });
const matches = (name) => !search.q || name.toLowerCase().includes(search.q.toLowerCase());
const gameName = (id) => { const g = ((st.state && st.state.games) || []).find((x) => x.id === id); return g ? g.short : id; };

function searchRow(u) {
  const r = el("div", "r");
  const here = st.state && u.game === st.state.game;
  r.appendChild(pic(u.user, u.status));
  const t = el("div", "r-txt");
  t.appendChild(el("span", "r-name", u.user));
  const where = !u.status ? "Desconectado" : here ? statusWords[u.status] : "Jugando " + gameName(u.game);
  t.appendChild(el("span", "r-sub " + (u.status ? u.status : "off"), where));
  r.appendChild(t);
  const act = el("div", "act keep");
  const star = button("star" + (u.friend ? " on" : ""), "★", async () => {
    if (fail(await api().Friend(u.user, !u.friend))) {
      u.friend = !u.friend;
      note(u.friend ? "Añadido a amigos" : "Quitado de amigos", u.user, "good", null, 4, u.user);
      renderOnline();
    }
  });
  star.title = u.friend ? "Quitar de amigos" : "Añadir a amigos";
  if (!u.blocked) act.appendChild(star);
  act.appendChild(u.blocked ? button("txtbtn", "Desbloquear", () => setBlocked(u.user, false, u)) : blockButton(u.user, u));
  r.appendChild(act);
  return r;
}

// [block] the button asks twice (a stray press on the pad must not block anybody)
function blockButton(user, rec) {
  let armed = 0;
  const b = button("blockbtn", "⊘", () => {
    if (Date.now() - armed > 4000) {
      armed = Date.now();
      b.textContent = "¿Bloquear?";
      setTimeout(() => { if (Date.now() - armed >= 4000) b.textContent = "⊘"; }, 4100);
      return;
    }
    setBlocked(user, true, rec);
  });
  b.title = "Bloquear a " + user;
  return b;
}
async function setBlocked(user, block, rec) {
  if (!fail(await api().Block(user, block))) return;
  if (rec) rec.blocked = block;
  if (block) { st.blocked = st.blocked.concat([user]); st.players = st.players.filter((p) => !same(p.user, user)); st.friends = st.friends.filter((f) => !same(f, user)); }
  else st.blocked = st.blocked.filter((b) => !same(b, user));
  note(block ? user + " bloqueado" : user + " desbloqueado", block ? "No podréis invitaros ni estar en la misma sala, y no verás su chat." : "Ya podéis volver a jugar.", block ? "" : "good", null, 6);
  renderOnline();
  renderBlocked();
}
function renderBlocked() {
  const box = $("blocked-list");
  box.replaceChildren();
  if (!st.blocked.length) { box.appendChild(el("span", "dim", "Nadie.")); return; }
  for (const name of st.blocked.slice().sort((a, b) => a.localeCompare(b))) {
    const r = el("div", "brow");
    r.appendChild(el("span", null, name));
    r.appendChild(button("txtbtn", "Desbloquear", () => setBlocked(name, false)));
    box.appendChild(r);
  }
}

async function invite(p) {
  if (p.status === "playing") { note(p.user + " está jugando", "Prueba cuando termine su pelea.", ""); return; }
  if (fail(await api().Invite(p.user))) note("Invitación enviada", "Ahora le toca a " + p.user + ".", "good", null, 6, p.user);
}

function renderOnline() { keepFocus(drawOnline); }
function drawOnline() {
  renderFriends();
  const logged = st.state && st.state.loggedIn;
  $("login").classList.toggle("hidden", logged);
  $("lobby").classList.toggle("hidden", !logged);
  const pending = st.invites.size;
  $("nav-badge").textContent = String(pending);
  $("nav-badge").classList.toggle("hidden", pending === 0);
  if (!logged) return;
  $("conn").className = "conn " + (st.connected ? "on" : "off");
  $("conn-text").textContent = st.connected ? "Conectado como " + st.me : "Se cortó la conexión, reconectando…";
  $("s-waiting").checked = myStatus() === "waiting";
  $("s-waiting").disabled = !st.connected || !!st.room;

  const pl = $("players");
  pl.replaceChildren();
  $("p-count").textContent = st.players.length ? "(" + st.players.length + ")" : "";
  const others = st.players.filter((p) => !same(p.user, st.me));
  const shown = st.players.filter((p) => matches(p.user));
  if (!others.length && !search.q) pl.appendChild(el("div", "none", "Por ahora no hay nadie más. Cuando alguien entre, aparecerá aquí."));
  if (search.q && !shown.length && search.q.length < 2) pl.appendChild(el("div", "none", "Nadie conectado se llama así."));
  for (const p of shown) {
    const me = same(p.user, st.me);
    const r = el("div", "r");
    r.appendChild(pic(p.user, p.status));
    const t = el("div", "r-txt");
    t.appendChild(el("span", "r-name", p.user));
    const other = p.build !== st.state.build;
    t.appendChild(el("span", "r-sub " + (other ? "" : p.status), other ? "Tiene otra versión del juego" : statusWords[p.status]));
    r.appendChild(t);
    if (me) { r.appendChild(el("span", "you", "tú")); }
    else {
      const act = el("div", "act" + (p.friend ? " keep" : ""));
      const star = button("star" + (p.friend ? " on" : ""), "★", async () => fail(await api().Friend(p.user, !p.friend)));
      star.title = p.friend ? "Quitar de amigos" : "Añadir a amigos";
      act.appendChild(star);
      if (!other && p.status !== "playing" && st.connected) act.appendChild(button("txtbtn", "Invitar", () => invite(p)));
      act.appendChild(blockButton(p.user, p));
      r.appendChild(act);
    }
    pl.appendChild(fresh("players", p.user.toLowerCase(), r));
  }
  // accounts the server found that are not in the list above (offline, or in another game)
  if (search.q.length >= 2) {
    const extra = search.users.filter((u) => !same(u.user, st.me) && !shown.some((p) => same(p.user, u.user)));
    if (extra.length) {
      pl.appendChild(el("div", "sub-h", "Otras cuentas"));
      for (const u of extra) pl.appendChild(fresh("players", "s:" + u.user.toLowerCase(), searchRow(u)));
    } else if (!shown.length) pl.appendChild(el("div", "none", "No hay ninguna cuenta con «" + search.q + "» en el nombre."));
  }
  drawn("players");

  // rooms, or my room
  const inRoom = !!st.room;
  $("rooms-box").classList.toggle("hidden", inRoom);
  $("room-panel").classList.toggle("hidden", !inRoom);
  if (!inRoom) {
    const rl = $("rooms");
    rl.replaceChildren();
    if (!st.rooms.length) rl.appendChild(el("div", "none", "No hay salas abiertas. Crea una y pásale el nombre y la contraseña a tus amigos."));
    for (const room of st.rooms) {
      const r = el("div", "r");
      const t = el("div", "r-txt");
      const name = el("span", "r-name", room.name);
      if (room.locked) name.appendChild(el("span", "lockmark", "privada"));
      t.appendChild(name);
      t.appendChild(el("span", "r-sub", room.members.length + " de " + room.max + " · la creó " + room.owner + (room.match ? " · peleando ahora" : "")));
      r.appendChild(t);
      const full = room.members.length >= room.max, other = room.build !== st.state.build;
      const act = el("div", "act keep");
      if (other) act.appendChild(el("span", "you", "otra versión"));
      else if (full) act.appendChild(el("span", "you", "llena"));
      else if (st.connected) act.appendChild(button("txtbtn", "Entrar", () => joinRoom(room)));
      r.appendChild(act);
      rl.appendChild(fresh("rooms", room.id, r));
    }
    drawn("rooms");
    return;
  }
  const room = st.room;
  $("room-name").textContent = room.name;
  const owner = same(room.owner, st.me);
  const q = $("queue");
  q.replaceChildren();
  room.members.forEach((m, i) => {
    const row = el("div", "q" + (i < 2 ? " next" : ""));
    row.appendChild(el("span", "num", String(i + 1)));
    const pc = pic(m, "online");
    pc.dataset.voiceUser = m.toLowerCase(); // [voice] lights up while they talk
    row.appendChild(pc);
    const t = el("div", "r-txt");
    const nm = el("span", "r-name", m);
    if (same(m, room.owner)) nm.appendChild(el("span", "owner", "anfitrión"));
    t.appendChild(nm);
    t.appendChild(el("span", "r-sub", i < 2 ? (room.match ? "peleando" : "pelea la siguiente") : "esperando turno"));
    row.appendChild(t);
    if (owner && !same(m, room.owner)) row.appendChild(button("txtbtn", "Sacar", async () => fail(await api().Kick(m))));
    q.appendChild(fresh("queue", m.toLowerCase(), row));
  });
  drawn("queue");
  talkingLights();
  const sb = $("btn-start");
  sb.classList.toggle("hidden", !owner);
  sb.disabled = !!room.match || room.members.length < 2;
  sb.textContent = room.match ? "Pelea en curso" : room.members.length < 2 ? "Esperando a alguien más" : room.members[0] + " contra " + room.members[1] + ": empezar";
}

$("btn-login").addEventListener("click", () => doLogin(false));
$("btn-register").addEventListener("click", () => doLogin(true));
$("l-pass").addEventListener("keydown", (e) => { if (e.key === "Enter") doLogin(false); });
async function doLogin(register) {
  $("l-err").textContent = "";
  const u = $("l-user").value.trim(), p = $("l-pass").value;
  const msg = register ? await api().Register(u, p) : await api().Login(u, p);
  $("l-pass").value = "";
  if (msg) { $("l-err").textContent = msg; return; }
  refresh();
}
$("btn-logout").addEventListener("click", async () => { await api().Logout(); st.players = []; st.rooms = []; st.room = null; refresh(); });
$("btn-server").addEventListener("click", async () => {
  const msg = await api().SetServer($("s-server").value);
  $("srv-err").textContent = msg || "";
  if (!msg) note("Servidor guardado", "Entra otra vez con tu cuenta de ese servidor.", "good");
  refresh();
});
$("s-waiting").addEventListener("change", async (e) => { fail(await api().SetWaiting(e.target.checked)); });
$("btn-leave").addEventListener("click", async () => { fail(await api().LeaveRoom()); });
$("btn-start").addEventListener("click", async () => { fail(await api().StartRoom()); });
$("chat-in").addEventListener("keydown", async (e) => {
  if (e.key !== "Enter") return;
  const t = e.target.value.trim();
  e.target.value = "";
  if (t) fail(await api().Chat(t));
});

let roomMax = 2;
document.querySelectorAll("#r-max button").forEach((b) => b.addEventListener("click", () => {
  roomMax = parseInt(b.dataset.max, 10);
  document.querySelectorAll("#r-max button").forEach((x) => x.classList.toggle("on", x === b));
}));
$("btn-newroom").addEventListener("click", () => { $("r-err").textContent = ""; $("r-name").value = st.me ? "La sala de " + st.me : ""; $("m-room").classList.remove("hidden"); $("r-name").focus(); });
$("btn-room-cancel").addEventListener("click", () => $("m-room").classList.add("hidden"));

// ---------------- online statistics ----------------
function playedTime(secs) {
  if (secs < 3600) return Math.round(secs / 60) + " min";
  const h = secs / 3600;
  return (h < 10 ? h.toFixed(1).replace(".", ",") : Math.round(h)) + " h";
}
function winRate(w, l) { return w + l ? Math.round((100 * w) / (w + l)) + "%" : "–"; }
async function openStats(user) {
  const r = await api().Stats(user || "");
  if (r.error) { fail(r.error); return; }
  const me = r.me;
  $("st-title").textContent = same(me.user, st.me) ? "Tus estadísticas" : "Estadísticas de " + me.user;
  const box = $("st-me");
  box.replaceChildren();
  for (const [v, label, cls] of [[me.wins, "Ganadas", "t-win"], [me.losses, "Perdidas", "t-loss"], [winRate(me.wins, me.losses), "Victorias"],
    [playedTime(me.seconds), "Jugado en línea"], [me.matches, "Partidas"], [me.rank ? "#" + me.rank : "–", "Puesto"]]) {
    const t = el("div", "tile " + (cls || ""));
    t.appendChild(el("b", null, String(v)));
    t.appendChild(el("span", null, label));
    box.appendChild(t);
  }
  const top = $("st-top");
  top.replaceChildren();
  if (!r.top.length) top.appendChild(el("div", "none", "Todavía nadie tiene peleas en línea. ¡Sé el primero!"));
  for (const p of r.top) {
    const row = el("div", "r" + (same(p.user, st.me) ? " mine" : ""));
    row.appendChild(el("span", "rank", "#" + p.rank));
    row.appendChild(pic(p.user));
    const t = el("div", "r-txt");
    t.appendChild(el("span", "r-name", p.user));
    t.appendChild(el("span", "r-sub", p.wins + " ganadas · " + p.losses + " perdidas · " + winRate(p.wins, p.losses) + " · " + playedTime(p.seconds)));
    row.appendChild(t);
    row.tabIndex = 0;
    row.addEventListener("click", () => openStats(p.user));
    row.addEventListener("keydown", (e) => { if (e.key === "Enter") openStats(p.user); });
    top.appendChild(row);
  }
  $("m-stats").classList.remove("hidden");
}
$("btn-stats").addEventListener("click", () => openStats(""));
$("btn-stats-close").addEventListener("click", () => $("m-stats").classList.add("hidden"));
$("btn-room-ok").addEventListener("click", async () => {
  const msg = await api().CreateRoom($("r-name").value, $("r-pass").value, roomMax);
  $("r-pass").value = "";
  if (msg) { $("r-err").textContent = msg; return; }
  $("m-room").classList.add("hidden");
});

let joining = null;
async function joinRoom(r) {
  if (!r.locked) { fail(await api().JoinRoom(r.id, "")); return; }
  joining = r;
  $("mp-title").textContent = r.name;
  $("mp-err").textContent = "";
  $("m-pass").classList.remove("hidden");
  $("mp-pass").focus();
}
$("mp-pass").addEventListener("keydown", (e) => { if (e.key === "Enter") $("btn-mp-ok").click(); });
$("btn-mp-cancel").addEventListener("click", () => $("m-pass").classList.add("hidden"));
$("btn-mp-ok").addEventListener("click", async () => {
  const msg = await api().JoinRoom(joining.id, $("mp-pass").value);
  $("mp-pass").value = "";
  if (msg) { $("mp-err").textContent = msg; return; }
  $("m-pass").classList.add("hidden");
});

function chatLine(from, text) {
  const log = $("chat-log");
  const line = el("div");
  if (from) line.appendChild(el("b", null, from));
  else line.className = "sys";
  line.appendChild(document.createTextNode(text));
  log.appendChild(line);
  while (log.childNodes.length > 200) log.removeChild(log.firstChild);
  log.scrollTop = log.scrollHeight;
}

// ---------------- [voice] the voice chat ----------------
const vc = { s: null, peopleKey: "" };

function onVoice(v) {
  vc.s = v;
  const active = !!v.channel;
  $("voice-bar").classList.toggle("hidden", !active);
  $("lobby").classList.toggle("with-voice", active);
  const mic = $("v-mic");
  mic.classList.toggle("off", v.muted);
  mic.classList.toggle("live", v.sending && !v.muted);
  $("v-mic-text").textContent = v.muted ? "Micrófono cerrado" : v.ptt ? "Mantén " + v.pttKey : "Micrófono abierto";
  for (const id of ["v-level", "v-level2"]) {
    const i = $(id);
    i.style.width = (v.muted ? 0 : v.level) + "%";
    i.classList.toggle("on", v.sending && !v.muted);
  }
  let s = "Conectando la voz…";
  if (!v.enabled) s = "Chat de voz desactivado (en Ajustes)";
  else if (!v.on) s = v.error ? "" : "Voz desconectada";
  else if (v.state === 2) s = "Voz conectada" + (v.ping >= 0 ? " · " + v.ping + " ms al servidor" : "");
  else if (v.state < 0) s = "La voz falló";
  if (v.silent) s = "El anfitrión te silenció en la sala";
  $("v-status").textContent = s;
  $("v-err").textContent = v.error || "";
  drawVoicePeople(v);
  talkingLights();
  if (!$("v-settings").classList.contains("hidden")) voiceFormHints();
}

// the others of the channel: rebuilt only when who is there (or my choices for them) changed; 10 times a second
// only their light and latency move
function drawVoicePeople(v) {
  const box = $("v-people");
  const owner = !!(st.room && same(st.room.owner, st.me));
  const key = JSON.stringify([owner, v.players.map((p) => [p.user, p.muted, p.volume, p.silent])]);
  if (key !== vc.peopleKey) {
    vc.peopleKey = key;
    keepFocus(() => {
      box.replaceChildren();
      if (!v.players.length) box.appendChild(el("span", "none", "Nadie más en la voz todavía"));
      for (const p of v.players.slice().sort((a, b) => a.slot - b.slot)) {
        const c = el("div", "vp" + (p.muted ? " muted" : ""));
        c.dataset.k = "voice:" + p.user.toLowerCase();
        c.dataset.user = p.user.toLowerCase();
        c.append(el("i", "vp-dot"), el("span", "vp-name", p.user), el("span", "vp-ms", ""));
        c.appendChild(button("txtbtn", p.muted ? "Oír" : "Silenciar", () => api().VoicePeer(p.user, p.volume, !p.muted)));
        const vol = el("input");
        vol.type = "range"; vol.min = "0"; vol.max = "200"; vol.step = "10"; vol.value = String(p.volume);
        vol.title = "Volumen de " + p.user;
        vol.addEventListener("change", () => api().VoicePeer(p.user, +vol.value, p.muted));
        c.appendChild(vol);
        if (owner) c.appendChild(button("txtbtn", p.silent ? "Devolverle la voz" : "Silenciar en la sala", async () => fail(await api().RoomMute(p.user, !p.silent))));
        else if (p.silent) c.appendChild(el("span", "vp-tag", "silenciado por el anfitrión"));
        box.appendChild(c);
      }
    });
  }
  for (const p of v.players) {
    const c = box.querySelector('.vp[data-user="' + CSS.escape(p.user.toLowerCase()) + '"]');
    if (!c) continue;
    c.classList.toggle("talking", p.talking && !p.muted);
    const ms = c.querySelector(".vp-ms");
    ms.textContent = p.ms >= 0 ? p.ms + " ms" : "";
    ms.title = p.ms >= 0 ? "de su boca a tu oído (estimado)" + (p.loss > 50 ? "; pierde paquetes: se rellenan" : "") : "";
    ms.classList.toggle("bad", p.ms > 250 || p.loss > 100);
  }
}

// the avatars of the room's queue glow while that player talks (me too, while my voice goes out)
function talkingLights() {
  const v = vc.s;
  document.querySelectorAll("[data-voice-user]").forEach((e) => {
    const p = v && v.players.find((x) => same(x.user, e.dataset.voiceUser));
    e.classList.toggle("talking", !!v && (!!(p && p.talking && !p.muted) || (same(e.dataset.voiceUser, st.me) && v.sending && !v.muted)));
  });
}

$("v-mic").addEventListener("click", () => api().VoiceToggleMute());

// Ajustes > Chat de voz
function fillDevices(sel, list, cur) {
  sel.replaceChildren();
  const def = el("option", "", "La del sistema");
  def.value = "";
  sel.appendChild(def);
  for (const n of list || []) { const o = el("option", "", n); o.value = n; sel.appendChild(o); }
  if (cur && !(list || []).includes(cur)) { const o = el("option", "", cur + " (no conectado)"); o.value = cur; sel.appendChild(o); }
  sel.value = cur || "";
}
async function voiceSettingsShown() {
  const v = vc.s || await api().VoiceState();
  vc.s = v;
  $("s-v-on").checked = v.enabled;
  $("s-v-mode").value = v.ptt ? "ptt" : "voice";
  $("s-v-sense").value = v.sense;
  $("s-v-gain").value = v.micGain;
  $("s-v-vol").value = v.volume;
  $("s-v-echo").checked = v.echo;
  $("opt-echo").classList.toggle("hidden", !(st.state && st.state.platform === "macOS"));
  voiceFormHints();
  const d = await api().VoiceDevices();
  fillDevices($("s-v-mic"), d.mics, v.mic);
  fillDevices($("s-v-spk"), d.speakers, v.speaker);
}
function voiceFormHints() {
  const v = vc.s, ptt = $("s-v-mode").value === "ptt";
  $("v-mode-hint").textContent = ptt
    ? "Mantén " + (v ? v.pttKey : "la tecla") + " mientras hablas, también con el juego delante (el juego sigue recibiendo la tecla)."
    : "Se abre sola cuando hablas. Durante la partida, Shift+M cierra o abre el micrófono (suena un tono).";
  $("opt-sense").classList.toggle("hidden", ptt);
  $("s-v-gain-v").textContent = $("s-v-gain").value + "%";
  $("s-v-vol-v").textContent = $("s-v-vol").value + "%";
}
function saveVoice() {
  const v = vc.s || {};
  api().VoiceSave({
    enabled: $("s-v-on").checked, muted: !!v.muted, ptt: $("s-v-mode").value === "ptt", sense: +$("s-v-sense").value,
    micGain: +$("s-v-gain").value, volume: +$("s-v-vol").value, echo: $("s-v-echo").checked,
    mic: $("s-v-mic").value, speaker: $("s-v-spk").value,
  });
}
for (const id of ["s-v-on", "s-v-mode", "s-v-sense", "s-v-gain", "s-v-vol", "s-v-echo", "s-v-mic", "s-v-spk"]) {
  $(id).addEventListener("change", () => { voiceFormHints(); saveVoice(); });
}
for (const id of ["s-v-gain", "s-v-vol"]) $(id).addEventListener("input", voiceFormHints);

// ---------------- live events from Go ----------------
function onOnline(ev) {
  switch (ev.type) {
    case "connected": st.connected = true; break;
    case "disconnected": st.connected = false; break;
    case "hello": st.connected = true; if (ev.user) st.me = ev.user; break;
    case "logged_out": st.connected = false; st.players = []; st.rooms = []; st.room = null; refresh(); return;
    case "replaced": st.connected = false; note("Abriste el launcher en otro sitio", "Esta ventana deja de recibir el lobby.", "bad", null, 10); break;
    case "lobby": {
      // the server sends one list for everybody plus my friends; friends go first
      const fr = new Set((ev.friends || []).map((f) => f.toLowerCase()));
      // [block] the ones I blocked are not shown
      st.blocked = ev.blocked || [];
      const bl = new Set(st.blocked.map((b) => b.toLowerCase()));
      st.players = (ev.players || []).filter((p) => !bl.has(p.user.toLowerCase())).map((p) => Object.assign({}, p, { friend: fr.has(p.user.toLowerCase()) }));
      st.players.sort((a, b) => (b.friend - a.friend) || a.user.toLowerCase().localeCompare(b.user.toLowerCase()));
      st.rooms = ev.rooms || [];
      st.friends = ev.friends || [];
      break;
    }
    case "room": {
      const before = st.room ? st.room.members.slice() : null;
      st.room = ev.room;
      if (before) {
        for (const m of ev.room.members) if (!before.some((b) => same(b, m))) chatLine("", m + " entró en la sala.");
        for (const b of before) if (!ev.room.members.some((m) => same(b, m))) chatLine("", b + " se fue.");
      }
      break;
    }
    case "room_left": if (st.room && st.room.id === ev.id) { st.room = null; $("chat-log").replaceChildren(); } break;
    case "chat": chatLine(ev.from, ev.text); break;
    case "invite": {
      const inv = ev.invite;
      if (st.invites.has(inv.id)) break;
      st.invites.set(inv.id, inv);
      const done = () => { st.invites.delete(inv.id); renderOnline(); };
      // during an offline game accepting closes it: say so before the player chooses
      const text = (inv.settings && inv.settings.fps60 ? "Peleas a 60 fps." : "Peleas a 30 fps.") +
        (st.state && st.state.playing ? " Si aceptas, se cierra tu partida actual (lo que no hayas guardado se pierde)." : "");
      const n = note(inv.from + " quiere pelear contigo", text, "good", [
        { label: "Aceptar", cls: "green", fn: async () => { done(); fail(await api().Respond(inv.id, true)); } },
        { label: "Ahora no", fn: async () => { done(); await api().Respond(inv.id, false); } },
      ], 55, inv.from);
      n.dataset.invite = inv.id;
      // with a controller: A accepts at once (B, in pad.js, says "not now")
      const yes = n.querySelector(".btn.green");
      if (yes) setTimeout(() => yes.focus({ preventScroll: true }), 50);
      break;
    }
    case "invite_declined": note(ev.user + " no puede ahora", "", "", null, 5, ev.user); break;
    case "invite_expired": st.invites.delete(ev.id); break;
    case "match_end": refresh(); break;
    case "error": note("Aviso", ev.message, "bad"); break;
  }
  renderMe();
  renderOnline();
  renderBlocked();
}

function boot() {
  // hands dropped files to Go (OnFileDrop in app.go), wherever they land in the window
  rt().OnFileDrop(() => {}, false);
  rt().EventsOn("online", onOnline);
  rt().EventsOn("voice", onVoice); // [voice] 10 times a second while in a channel
  rt().EventsOn("toast", (t) => note(t.kind === "bad" ? "Algo falló" : "Aviso", t.text, t.kind));
  rt().EventsOn("dlss", (d) => {
    if (d.skipped && d.skipped.length) note("Eso no es una DLL de DLSS que sirva", d.skipped.join(", "), "bad", null, 9);
    else if (d.added) note("DLSS añadido", "Se usará desde la próxima partida.", "good");
    refresh();
  });
  rt().EventsOn("game", (g) => {
    if (g.running) note("Abriendo el juego", "Back + Start en el mando (o Shift+Tab) te trae aquí durante la partida, para ver quién está en línea o invitar.", "good", null, 6);
    if (!g.running && g.code) note("El juego se cerró con un error", "Mira el registro: " + g.log, "bad", null, 12);
    $("btn-leave-match").classList.toggle("hidden", !(g.running && g.online));
    refresh();
  });
  // the game left the online session (nobody came to the menu, or the other player left): it goes on offline
  rt().EventsOn("game_offline", () => {
    $("btn-leave-match").classList.add("hidden");
    note("La partida online terminó", "El juego sigue abierto, sin conexión.", "", null, 8);
    refresh();
  });
  // [invite] answered in the game with the pad: the launcher's notice goes away
  rt().EventsOn("invite_answered", (a) => { st.invites.delete(a.id); document.querySelectorAll('.note[data-invite="' + a.id + '"]').forEach((n) => n.remove()); renderOnline(); });
  rt().EventsOn("install", onInstall);
  // a newer version of a game was published: one click updates it (it keeps settings, saves and the ISO's files)
  rt().EventsOn("update", (u) => note("Hay una versión nueva de " + gameName(u.game), "Versión " + u.version + (u.current ? " (tienes la " + u.current + ")" : "") + ".", "",
    [{ label: "Actualizar", cls: "blue", fn: () => api().AddGame(u.game) }], 20));
  // the launcher updated itself: the new version starts with the next launch, or now
  rt().EventsOn("launcher_update", (u) => {
    if (u.phase === "ready") note("Recomp " + u.version + " está lista", "Se usará la próxima vez que abras Recomp.", "good",
      [{ label: "Reiniciar ahora", cls: "blue", fn: async () => fail(await api().RestartForUpdate()) }], 30);
    else if (u.phase === "manual") note("Hay una versión nueva de Recomp", "Versión " + u.version + ": descárgala desde GitHub.", "", null, 20);
    else if (u.error) note("No pude actualizar Recomp", u.error, "bad", null, 20);
  });
  rt().EventsOn("overlay", (on) => { $("overlay-banner").classList.toggle("hidden", !on); if (on) show("online"); });
  rt().EventsOn("match_countdown", (m) => {
    $("mm-me").textContent = st.me || "Tú";
    $("mm-op").textContent = m.opponent || "Rival";
    $("m-match").classList.remove("hidden");
    let n = 3;
    const tick = () => {
      const t = $("mm-text");
      t.textContent = "Abriendo el juego en " + n + "…";
      t.classList.remove("tick"); void t.offsetWidth; t.classList.add("tick");
    };
    tick();
    const t = setInterval(() => { n--; if (n <= 0) { clearInterval(t); $("m-match").classList.add("hidden"); return; } tick(); }, 1000);
  });
  refresh().then(async () => {
    for (const ev of (await api().OnlineSnapshot()) || []) onOnline(ev);
    onVoice(await api().VoiceState());
  });
}

(function waitRuntime(n) {
  if (window.go && window.go.main && window.runtime) return boot();
  if (n > 200) { document.body.textContent = "No se pudo iniciar la ventana."; return; }
  setTimeout(() => waitRuntime(n + 1), 25);
})(0);
