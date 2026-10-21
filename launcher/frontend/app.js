"use strict";
// Recomp launcher interface. Talks to the Go side through Wails (window.go.main.App.* and runtime events).
// Rule: nothing that comes from the server or from other players is inserted as HTML -- only as text.

const api = () => window.go.main.App;
const rt = () => window.runtime;
const $ = (id) => document.getElementById(id);

const st = { state: null, me: "", players: [], rooms: [], friends: [], room: null, invites: new Map(), connected: false };

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

// ---------------- library page ----------------
// the quality presets and a few texts differ per system (State: platform, qualities, fullscreenKey)
function renderPlatform(s) {
  const sel = $("s-quality");
  if (s.qualities && sel.dataset.platform !== s.platform) {
    sel.dataset.platform = s.platform;
    sel.textContent = "";
    s.qualities.forEach((q, i) => { const o = el("option", null, q.name); o.value = String(i); sel.appendChild(o); });
  }
  $("banner-sub").textContent = "Dragon Ball Z (Beta 14 Rev2) para " + s.platform;
  $("full-hint").textContent = s.fullscreenKey + " cambia durante la partida.";
  const mac = s.platform === "macOS";
  if (mac) {
    $("dlss-label").textContent = "Partidas y registros";
    $("dlss-hint").textContent = s.dataFolder;
  }
  return mac;
}

function renderPlay() {
  const s = st.state;
  $("ver").textContent = "Launcher " + s.version;
  $("btn-play").disabled = !s.ready || s.playing;
  $("btn-play").textContent = s.playing ? "Jugando" : "Jugar";
  $("iso-line").textContent = s.ready ? s.iso : s.iso ? "No encuentro la ISO que elegiste." : "Necesitas tu ISO del mod (SLUS-219.78, Beta 14 Rev2).";
  $("btn-iso").textContent = s.ready ? "Cambiar ISO" : "Seleccionar ISO";
  const mac = renderPlatform(s);
  $("s-quality").value = String(s.settings.quality);
  $("q-hint").textContent = (s.qualities && s.qualities[s.settings.quality] || {}).hint || "";
  $("s-fps60").checked = s.settings.fps60;
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
  s.fps60 = $("s-fps60").checked;
  s.fullscreen = $("s-full").checked;
  s.netDelay = parseInt($("s-delay").value, 10);
  if (fail(await api().SaveSettings(s))) refresh();
}
["s-quality", "s-fps60", "s-full", "s-delay"].forEach((id) => $(id).addEventListener("change", saveSettings));
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
const gameNames = { dbzbt4: "DBZ BT4", br4: "Bloody Roar 4" };

function searchRow(u) {
  const r = el("div", "r");
  const here = u.game === "dbzbt4";
  r.appendChild(pic(u.user, u.status));
  const t = el("div", "r-txt");
  t.appendChild(el("span", "r-name", u.user));
  const where = !u.status ? "Desconectado" : here ? statusWords[u.status] : "Jugando " + (gameNames[u.game] || u.game);
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
  act.appendChild(star);
  r.appendChild(act);
  return r;
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
    row.appendChild(pic(m, "online"));
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
      st.players = (ev.players || []).map((p) => Object.assign({}, p, { friend: fr.has(p.user.toLowerCase()) }));
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
}

function boot() {
  // hands dropped files to Go (OnFileDrop in app.go), wherever they land in the window
  rt().OnFileDrop(() => {}, false);
  rt().EventsOn("online", onOnline);
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
  refresh().then(async () => { for (const ev of (await api().OnlineSnapshot()) || []) onOnline(ev); });
}

(function waitRuntime(n) {
  if (window.go && window.go.main && window.runtime) return boot();
  if (n > 200) { document.body.textContent = "No se pudo iniciar la ventana."; return; }
  setTimeout(() => waitRuntime(n + 1), 25);
})(0);
