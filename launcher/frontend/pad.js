"use strict";
// Driving the launcher with a controller (Gamepad API, standard mapping): D-pad or left stick moves between the
// controls, A presses, B closes a dialog or goes back to the game, LB/RB change tab, left/right change a list.
// The game ignores the pad while this window is in front, so nothing reaches the fight.

(function () {
  const A = 0, B = 1, LB = 4, RB = 5, UP = 12, DOWN = 13, LEFT = 14, RIGHT = 15;
  const FIRST = 380, REPEAT = 110; // ms before a held direction repeats, and between repeats
  const held = new Map(); // button -> time of the next repeat
  let running = false;

  function usingPad(on) { document.body.classList.toggle("pad", on); }
  document.addEventListener("mousemove", () => usingPad(false), { passive: true });

  function visible(e) {
    if (e.disabled || e.closest(".hidden")) return false;
    const r = e.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== "hidden";
  }
  function controls() {
    const modal = [...document.querySelectorAll(".modal")].find((m) => !m.classList.contains("hidden"));
    const scope = modal || document;
    return [...scope.querySelectorAll("button, input, select, .friend, a[href]")].filter(visible);
  }

  // the nearest control in a direction: distance along it, plus the sideways offset counted double
  function move(dx, dy) {
    const list = controls();
    if (!list.length) return;
    const cur = document.activeElement;
    if (!list.includes(cur)) { focus(list.find((e) => e.closest("main")) || list[0]); return; }
    const c = cur.getBoundingClientRect();
    const cx = c.left + c.width / 2, cy = c.top + c.height / 2;
    let best = null, score = Infinity;
    for (const e of list) {
      if (e === cur) continue;
      const r = e.getBoundingClientRect();
      const ex = r.left + r.width / 2, ey = r.top + r.height / 2;
      const along = (ex - cx) * dx + (ey - cy) * dy;
      if (along <= 4) continue;
      const side = Math.abs((ex - cx) * dy) + Math.abs((ey - cy) * dx);
      const s = along + side * 2;
      if (s < score) { score = s; best = e; }
    }
    if (best) focus(best);
  }
  function focus(e) {
    if (!e.hasAttribute("tabindex") && !/^(BUTTON|INPUT|SELECT|A)$/.test(e.tagName)) e.tabIndex = -1;
    e.focus({ preventScroll: true });
    e.scrollIntoView({ block: "nearest", inline: "nearest", behavior: "smooth" });
  }

  function press() {
    const e = document.activeElement;
    if (!e || e === document.body) { move(0, 1); return; }
    if (e.tagName === "SELECT") { step(1); return; }
    if (e.tagName === "INPUT" && e.type !== "checkbox") return; // text: the keyboard writes there
    e.click();
  }
  function step(d) { // left/right on a list or a slider changes its value
    const e = document.activeElement;
    if (e && e.tagName === "INPUT" && e.type === "range") {
      const v = Math.min(+e.max, Math.max(+e.min, +e.value + d * (+e.step || 1)));
      if (v !== +e.value) { e.value = v; e.dispatchEvent(new Event("input")); e.dispatchEvent(new Event("change")); }
      return true;
    }
    if (!e || e.tagName !== "SELECT") return false;
    const i = Math.min(e.options.length - 1, Math.max(0, e.selectedIndex + d));
    if (i !== e.selectedIndex) { e.selectedIndex = i; e.dispatchEvent(new Event("change")); }
    return true;
  }
  function back() {
    const modal = [...document.querySelectorAll(".modal")].find((m) => !m.classList.contains("hidden"));
    if (modal) { const c = modal.querySelector("[id$=cancel]"); if (c) c.click(); return; }
    const inv = document.querySelector(".note[data-invite]:not(.out)"); // an invitation waiting: B = "Ahora no"
    if (inv) { const no = inv.querySelector(".btn:not(.green)"); if (no) no.click(); return; }
    if (!$("overlay-banner").classList.contains("hidden")) api().HideOverlay();
  }
  function tab(d) {
    const tabs = [...document.querySelectorAll(".tab")];
    const i = tabs.findIndex((t) => t.classList.contains("active"));
    const t = tabs[(i + d + tabs.length) % tabs.length];
    t.click();
    focus(t);
  }

  const actions = {
    [UP]: () => move(0, -1), [DOWN]: () => move(0, 1),
    [LEFT]: () => step(-1) || move(-1, 0), [RIGHT]: () => step(1) || move(1, 0),
    [A]: press, [B]: back, [LB]: () => tab(-1), [RB]: () => tab(1),
  };
  const repeats = new Set([UP, DOWN, LEFT, RIGHT]);

  function frame(now) {
    const pads = [...navigator.getGamepads()].filter(Boolean);
    if (!pads.length) { running = false; held.clear(); return; }
    requestAnimationFrame(frame);
    if (!document.hasFocus()) { held.clear(); return; } // minimised during a game: the pad is the game's
    const down = new Set();
    for (const p of pads) {
      p.buttons.forEach((b, i) => { if (b.pressed) down.add(i); });
      const [x, y] = p.axes;
      if (y < -0.6) down.add(UP); if (y > 0.6) down.add(DOWN);
      if (x < -0.6) down.add(LEFT); if (x > 0.6) down.add(RIGHT);
    }
    for (const k of [...held.keys()]) if (!down.has(k)) held.delete(k);
    for (const k of down) {
      const fn = actions[k];
      if (!fn) continue;
      if (!held.has(k)) { held.set(k, now + FIRST); usingPad(true); fn(); }
      else if (repeats.has(k) && now >= held.get(k)) { held.set(k, now + REPEAT); fn(); }
    }
  }
  function begin() { if (!running) { running = true; requestAnimationFrame(frame); } }
  window.addEventListener("gamepadconnected", begin);
  if ([...navigator.getGamepads()].some(Boolean)) begin();
})();
