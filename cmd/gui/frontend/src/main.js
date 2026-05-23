import {Events} from "@wailsio/runtime";
import {ListSessions, CreateSession, KillSession} from "../bindings/github.com/naglezhang/fingersaver/cmd/gui/tmuxservice.js";
import {AttachSession, DetachSession, WriteInput, ResizeTerminal} from "../bindings/github.com/naglezhang/fingersaver/cmd/gui/terminalservice.js";
import {SendMessage, LoadChatHistory, StartMonitor} from "../bindings/github.com/naglezhang/fingersaver/cmd/gui/chatservice.js";

import {Terminal} from "@xterm/xterm";
import {FitAddon} from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {marked} from "marked";

// btoa doesn't handle multi-byte Unicode — use TextEncoder for proper UTF-8→base64
function toBase64(str) {
    return btoa(String.fromCharCode(...new TextEncoder().encode(str)));
}

const terminals = new Map();
let activeTab = null;

const chatPanel    = document.getElementById("chat-panel");
const chatMessages = document.getElementById("chat-messages");
const chatInput    = document.getElementById("chat-input");
const btnToggle    = document.getElementById("btn-toggle");
const btnSend      = document.getElementById("btn-send");
const tabsEl       = document.getElementById("tabs");
const terminalsEl  = document.getElementById("terminals");
const emptyState   = document.getElementById("empty-state");
const btnNew       = document.getElementById("btn-new");
const btnRefresh   = document.getElementById("btn-refresh");
const btnStart     = document.getElementById("btn-start");

btnToggle.addEventListener("click", () => {
    chatPanel.classList.toggle("collapsed");
    btnToggle.classList.toggle("collapsed", chatPanel.classList.contains("collapsed"));
    setTimeout(refitAll, 250);
});

chatInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && chatInput.value.trim()) { sendChat(chatInput.value); chatInput.value = ""; }
});
btnSend.addEventListener("click", () => {
    if (chatInput.value.trim()) { sendChat(chatInput.value); chatInput.value = ""; }
});

function sendChat(text) {
    // &session: monitor mode — don't create assistant bubble.
    if (text.trim().startsWith("&")) {
        addChatMessage("user", text);
        SendMessage(text).catch(err => addChatMessage("system", "Error: " + err));
        chatInput.disabled = false;
        chatInput.focus();
        return;
    }

    // @session: switch terminal tab.
    const mention = text.trim().match(/^@(\S+)/);
    if (mention) {
        switchToTab(mention[1]);
    }

    addChatMessage("user", text);
    chatInput.disabled = true;
    addChatMessage("assistant", "");
    SendMessage(text).catch(err => { appendToLastMsg("[error: " + err + "]"); chatInput.disabled = false; });
}

Events.On("chat:text", (ev) => appendToLastMsg(ev.data));
Events.On("chat:tool_call", (ev) => { const d = JSON.parse(ev.data); appendToLastMsg("\n\n**▸ " + d.tool + "**\n"); });
Events.On("chat:tool_result", (ev) => { const d = JSON.parse(ev.data); appendToLastMsg("\n```\n" + d.result.substring(0, 500) + "\n```\n"); });
Events.On("chat:done", () => { chatInput.disabled = false; chatInput.focus(); });
Events.On("chat:error", (ev) => { appendToLastMsg("[error: " + ev.data + "]"); chatInput.disabled = false; });
Events.On("chat:init_error", (ev) => { addChatMessage("system", "Init error: " + ev.data); });
Events.On("chat:system", (ev) => { addChatMessage("system", ev.data); });
Events.On("chat:switch_tab", (ev) => { if (ev.data) switchToTab(ev.data); });

function addChatMessage(role, text) {
    const div = document.createElement("div");
    div.className = "chat-msg " + role;
    if (role === "assistant") {
        div.innerHTML = marked.parse(text || "");
        div.dataset.raw = text || "";
    } else {
        div.textContent = (role === "user" ? "> " : "") + text;
    }
    chatMessages.appendChild(div);
    chatMessages.scrollTop = chatMessages.scrollHeight;
    return div;
}

function appendToLastMsg(text) {
    const msgs = chatMessages.querySelectorAll(".chat-msg.assistant");
    if (msgs.length === 0) return;
    const last = msgs[msgs.length - 1];
    last.dataset.raw = (last.dataset.raw || "") + text;
    last.innerHTML = marked.parse(last.dataset.raw);
    chatMessages.scrollTop = chatMessages.scrollHeight;
}

function addTab(name) {
    const wrapper = document.createElement("div");
    wrapper.className = "terminal-wrapper"; wrapper.id = "term-" + name;
    terminalsEl.appendChild(wrapper);
    const term = new Terminal({ fontSize: 13, fontFamily: '"SF Mono", "JetBrains Mono", Menlo, Monaco, monospace', theme: { background: "#0c0c0e", foreground: "#e4e4e7", cursor: "#34d399", cursorAccent: "#0c0c0e", selectionBackground: "rgba(52,211,153,0.2)", selectionForeground: "#e4e4e7", black: "#3f3f46", red: "#f87171", green: "#34d399", yellow: "#fbbf24", blue: "#60a5fa", magenta: "#c084fc", cyan: "#22d3ee", white: "#e4e4e7", brightBlack: "#71717a", brightRed: "#fca5a5", brightGreen: "#6ee7b7", brightYellow: "#fde68a", brightBlue: "#93c5fd", brightMagenta: "#d8b4fe", brightCyan: "#67e8f9", brightWhite: "#ffffff" }, cursorBlink: true, scrollback: 10000 });
    const fitAddon = new FitAddon(); term.loadAddon(fitAddon); term.open(wrapper); fitAddon.fit();
    term.onData((data) => WriteInput(name, toBase64(data)).catch(console.error));
    term.onResize(({cols, rows}) => ResizeTerminal(name, cols, rows).catch(console.error));
    Events.On("terminal:" + name, (ev) => { const bytes = atob(ev.data); const arr = new Uint8Array(bytes.length); for (let i = 0; i < bytes.length; i++) arr[i] = bytes.charCodeAt(i); term.write(arr); });
    Events.On("terminal:exit:" + name, () => { term.write("\r\n\x1b[33m[disconnected]\x1b[0m"); removeTab(name); });
    const ro = new ResizeObserver(() => { try { fitAddon.fit(); } catch (e) {} });
    ro.observe(wrapper);
    terminals.set(name, {term, fitAddon, wrapper, ro});
    const tab = document.createElement("div");
    tab.className = "tab"; tab.dataset.session = name;
    tab.innerHTML = '<span class="name"></span><button class="close">×</button>';
    tab.querySelector(".name").textContent = name;
    tab.addEventListener("click", (e) => { if (!e.target.classList.contains("close")) switchToTab(name); });
    tab.querySelector(".close").addEventListener("click", (e) => { e.stopPropagation(); closeTab(name); });
    tabsEl.appendChild(tab);
    emptyState.classList.add("hidden"); terminalsEl.classList.add("visible");
    switchToTab(name); term.focus();
}

function switchToTab(name) {
    if (!terminals.has(name)) return; activeTab = name;
    document.querySelectorAll(".tab").forEach(t => t.classList.toggle("active", t.dataset.session === name));
    document.querySelectorAll(".terminal-wrapper").forEach(w => w.classList.toggle("active", w.id === "term-" + name));
    setTimeout(() => forceResize(name), 0);
    const entry = terminals.get(name); if (entry) entry.term.focus();
}

function closeTab(name) { DetachSession(name).catch(() => {}); KillSession(name).catch(() => {}); removeTab(name); }

function removeTab(name) {
    const entry = terminals.get(name); if (entry) { if (entry.ro) entry.ro.disconnect(); entry.term.dispose(); entry.wrapper.remove(); terminals.delete(name); }
    const tab = document.querySelector(`.tab[data-session="${name}"]`); if (tab) tab.remove();
    if (activeTab === name) { const r = Array.from(terminals.keys()); if (r.length > 0) switchToTab(r[r.length - 1]); else { activeTab = null; emptyState.classList.remove("hidden"); terminalsEl.classList.remove("visible"); } }
}

async function attachTo(name) {
    if (terminals.has(name)) { switchToTab(name); return; }
    try {
        await AttachSession(name);
        addTab(name);
        // Force sync actual size to PTY after attach (bypasses onResize debounce)
        forceResize(name);
    } catch (err) { addChatMessage("system", "Attach failed: " + err); }
}

function forceResize(name) {
    const entry = terminals.get(name);
    if (!entry) return;
    try { entry.fitAddon.fit(); } catch (e) {}
    ResizeTerminal(name, entry.term.cols, entry.term.rows).catch(console.error);
}

async function refreshSessions() {
    try {
        const sessions = await ListSessions();
        const alive = new Set((sessions || []).map(s => s.name));
        // Close tabs for sessions that no longer exist.
        for (const name of Array.from(terminals.keys())) {
            if (!alive.has(name)) removeTab(name);
        }
        // Attach new sessions.
        for (const s of (sessions || [])) {
            if (!terminals.has(s.name)) await attachTo(s.name);
        }
    } catch (err) { addChatMessage("system", "Refresh error: " + err); }
}

function refitAll() { for (const [name] of terminals) forceResize(name); }
btnNew.addEventListener("click", () => { const name = prompt("Session name:"); if (name) CreateSession(name, "").then(() => attachTo(name)).then(() => refreshSessions()).catch(err => addChatMessage("system", "Error: " + err)); });
btnRefresh.addEventListener("click", () => refreshSessions());
btnStart.addEventListener("click", () => refreshSessions());

// Load chat history from disk.
LoadChatHistory().then(msgs => {
    if (msgs && msgs.length > 0) {
        for (const m of msgs) addChatMessage(m.role, m.content);
    }
}).catch(() => {});

addChatMessage("system", "FingerSaver GUI ready.");
refreshSessions();

// Periodic session sync — close tabs for killed sessions, attach new ones.
setInterval(refreshSessions, 5000);

// ── @ and & autocomplete hints ──

const hintEl = document.createElement("div");
hintEl.id = "input-hint";
chatPanel.appendChild(hintEl);

chatInput.addEventListener("input", () => {
    const val = chatInput.value;
    const trimmed = val.trimStart();
    if (trimmed.startsWith("@")) {
        const partial = trimmed.slice(1).split(/\s/)[0].toLowerCase();
        const names = Array.from(terminals.keys()).filter(n => n.toLowerCase().startsWith(partial));
        const list = names.length ? names.map(n => "<div class='hint-item' data-name='" + n + "'>@" + n + "</div>").join("") : "<div class='hint-empty'>No matching sessions</div>";
        hintEl.innerHTML = "<div class='hint-title'>Switch to session</div>" + list;
        hintEl.classList.add("visible");
        hintEl.querySelectorAll(".hint-item").forEach(el => {
            el.addEventListener("click", () => {
                const name = el.dataset.name;
                const rest = trimmed.includes(" ") ? trimmed.slice(trimmed.indexOf(" ")) : " ";
                chatInput.value = "@" + name + rest;
                hintEl.classList.remove("visible");
                chatInput.focus();
            });
        });
    } else if (trimmed === "&") {
        const names = Array.from(terminals.keys());
        const list = names.map(n => "<div class='hint-item' data-name='" + n + "'>&" + n + "</div>").join("");
        hintEl.innerHTML = "<div class='hint-title'>Monitor session until idle</div>" + list;
        hintEl.classList.add("visible");
        hintEl.querySelectorAll(".hint-item").forEach(el => {
            el.addEventListener("click", () => {
                chatInput.value = "&" + el.dataset.name;
                hintEl.classList.remove("visible");
                chatInput.focus();
            });
        });
    } else {
        hintEl.classList.remove("visible");
    }
});

chatInput.addEventListener("blur", () => setTimeout(() => hintEl.classList.remove("visible"), 150));
chatInput.addEventListener("keydown", (e) => {
    if (e.key === "Escape") hintEl.classList.remove("visible");
});

// Keyboard shortcuts
document.addEventListener("keydown", (e) => {
    if (!(e.metaKey || e.ctrlKey)) return;

    // Cmd+B toggle chat panel
    if (e.key === "b") {
        e.preventDefault();
        chatPanel.classList.toggle("collapsed");
        btnToggle.classList.toggle("collapsed", chatPanel.classList.contains("collapsed"));
        // Two-phase resize: catch mid-transition and post-transition
        setTimeout(refitAll, 50);
        setTimeout(refitAll, 300);
        return;
    }

    // Cmd+1..9 switch to tab by index
    const num = parseInt(e.key);
    if (num >= 1 && num <= 9) {
        e.preventDefault();
        const names = Array.from(terminals.keys());
        if (num <= names.length) switchToTab(names[num - 1]);
        return;
    }

    // Cmd+L focus terminal input
    if (e.key === "l") {
        e.preventDefault();
        const entry = activeTab && terminals.get(activeTab);
        if (entry) entry.term.focus();
    }
});

// Cmd+J focus chat input
document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "j") {
        e.preventDefault();
        chatInput.focus();
    }
});
