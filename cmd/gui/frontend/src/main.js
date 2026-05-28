// Makro GUI — fetch/WebSocket version (no Wails bindings)
const BACKEND = window.makro?.backendUrl || 'http://127.0.0.1:7070';
const WS_URL = window.makro?.wsUrl || 'ws://127.0.0.1:7070';

import {Terminal} from "@xterm/xterm";
import {FitAddon} from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {marked} from "marked";

const terminals = new Map();
let activeTab = null;
let activeAssistantEl = null;
let currentToolEl = null;
let chatWs = null;

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

// ── API helpers ──
async function api(path, opts) { return fetch(BACKEND + path, opts).then(r => r.json()); }

// ── Chat ──
function sendChat(text) {
    if (text.trim().startsWith("&")) {
        addChatMessage("user", text);
        fetch(BACKEND + "/api/chat", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({text})}).catch(err => addChatMessage("system", "Error: " + err));
        chatInput.disabled = false;
        chatInput.focus();
        return;
    }
    const mention = text.trim().match(/^@(\S+)/);
    if (mention) { switchToTab(mention[1]); }
    addChatMessage("user", text);
    chatInput.disabled = true;
    activeAssistantEl = addChatMessage("assistant", "");
    currentToolEl = null;
    fetch(BACKEND + "/api/chat", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({text})}).catch(err => { appendToEl(activeAssistantEl, "[error: " + err + "]"); chatInput.disabled = false; });
}

function connectChatWs() {
    if (chatWs) return;
    chatWs = new WebSocket(WS_URL + "/ws/chat");
    chatWs.onmessage = (ev) => {
        try {
            const msg = JSON.parse(ev.data);
            if (msg.type === "ping") return;
            if (msg.type === "user") return;
            if (msg.type === "assistant") {
                if (currentToolEl) {
                    const newEl = document.createElement("div");
                    newEl.className = "chat-msg assistant";
                    newEl.dataset.raw = "";
                    if (currentToolEl.nextSibling) { chatMessages.insertBefore(newEl, currentToolEl.nextSibling); } else { chatMessages.appendChild(newEl); }
                    activeAssistantEl = newEl;
                    currentToolEl = null;
                }
                appendToEl(activeAssistantEl, msg.data);
            } else if (msg.type === "tool_call") {
                currentToolEl = addToolCall(msg.data);
            } else if (msg.type === "tool_result") {
                if (currentToolEl) setToolResult(currentToolEl, msg.data);
            } else if (msg.type === "done") {
                chatInput.disabled = false; chatInput.focus(); activeAssistantEl = null; currentToolEl = null;
            } else if (msg.type === "error") {
                appendToEl(activeAssistantEl, "[error: " + msg.data + "]"); chatInput.disabled = false;
            } else if (msg.type === "system") {
                addChatMessage("system", msg.data);
            }
        } catch (e) {}
    };
    chatWs.onclose = () => { chatWs = null; setTimeout(connectChatWs, 2000); };
    chatWs.onerror = () => { chatWs.close(); };
}

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

function appendToEl(el, text) {
    if (!el) return;
    el.dataset.raw = (el.dataset.raw || "") + text;
    el.innerHTML = marked.parse(el.dataset.raw);
    chatMessages.scrollTop = chatMessages.scrollHeight;
}

function addToolCall(toolName) {
    const div = document.createElement("div");
    div.className = "tool-call";
    const header = document.createElement("div");
    header.className = "tool-call-header";
    header.innerHTML = '<span class="arrow">▼</span><span class="tool-name"></span>';
    header.querySelector(".tool-name").textContent = toolName;
    header.addEventListener("click", () => div.classList.toggle("collapsed"));
    const body = document.createElement("div");
    body.className = "tool-call-body";
    div.appendChild(header);
    div.appendChild(body);
    const ref = currentToolEl || activeAssistantEl;
    if (ref && ref.nextSibling) { chatMessages.insertBefore(div, ref.nextSibling); } else { chatMessages.appendChild(div); }
    chatMessages.scrollTop = chatMessages.scrollHeight;
    return div;
}

function setToolResult(toolEl, result) {
    toolEl.querySelector(".tool-call-body").textContent = result.substring(0, 1000);
    chatMessages.scrollTop = chatMessages.scrollHeight;
}

// ── Terminal tabs ──
function addTab(name) {
    const wrapper = document.createElement("div");
    wrapper.className = "terminal-wrapper"; wrapper.id = "term-" + name;
    terminalsEl.appendChild(wrapper);
    const term = new Terminal({
        fontSize: 13,
        fontFamily: '"SF Mono", "JetBrains Mono", Menlo, Monaco, "PingFang SC", "Noto Sans CJK SC", monospace',
        theme: {
            background: "#0c0c0e", foreground: "#e4e4e7", cursor: "#34d399", cursorAccent: "#0c0c0e",
            selectionBackground: "rgba(52,211,153,0.2)", selectionForeground: "#e4e4e7",
            black: "#3f3f46", red: "#f87171", green: "#34d399", yellow: "#fbbf24",
            blue: "#60a5fa", magenta: "#c084fc", cyan: "#22d3ee", white: "#e4e4e7",
            brightBlack: "#71717a", brightRed: "#fca5a5", brightGreen: "#6ee7b7",
            brightYellow: "#fde68a", brightBlue: "#93c5fd", brightMagenta: "#d8b4fe",
            brightCyan: "#67e8f9", brightWhite: "#ffffff"
        },
        cursorBlink: true, scrollback: 10000,
    });
    const fitAddon = new FitAddon(); term.loadAddon(fitAddon);
    term.open(wrapper);

    // WebSocket for this terminal (sin-golang pattern: binary frames)
    const ws = new WebSocket(WS_URL + "/ws/xterm/" + name + "?cols=" + term.cols + "&rows=" + term.rows);
    ws.binaryType = "arraybuffer";

    ws.onmessage = (ev) => {
        // Binary frame = PTY output
        if (ev.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(ev.data));
        }
    };
    ws.onclose = () => {
        term.write("\r\n\x1b[33m[disconnected]\x1b[0m");
        removeTab(name);
    };

    term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(new TextEncoder().encode(data));
        }
    });

    // Copy/paste
    wrapper.addEventListener("keydown", (e) => {
        const mod = e.metaKey || e.ctrlKey;
        if (mod && e.key.toLowerCase() === "c") {
            const sel = term.getSelection();
            if (sel) { e.preventDefault(); e.stopPropagation(); navigator.clipboard.writeText(sel).catch(() => {}); }
        } else if (mod && e.key.toLowerCase() === "v") {
            e.preventDefault(); e.stopPropagation();
            navigator.clipboard.readText().then(text => { if (text && ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(text)); }).catch(() => {});
        }
    }, true);

    // Resize
    term.onResize(({cols, rows}) => {
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({type: "resize", cols, rows}));
        }
    });
    const ro = new ResizeObserver(() => {
        if (wrapper.offsetWidth === 0 || wrapper.offsetHeight === 0) return;
        try { fitAddon.fit(); } catch (e) {}
    });
    ro.observe(wrapper);

    terminals.set(name, {term, fitAddon, wrapper, ro, ws});

    // Tab UI
    const tab = document.createElement("div");
    tab.className = "tab"; tab.dataset.session = name;
    tab.innerHTML = '<span class="name"></span><button class="close">×</button>';
    tab.querySelector(".name").textContent = name;
    tab.addEventListener("click", (e) => { if (!e.target.classList.contains("close")) switchToTab(name); });
    tab.querySelector(".close").addEventListener("click", (e) => { e.stopPropagation(); closeTab(name); });
    tabsEl.appendChild(tab);
    emptyState.classList.add("hidden"); terminalsEl.classList.add("visible");
    switchToTab(name);
    fitAddon.fit();
    term.focus();
}

function switchToTab(name) {
    if (!terminals.has(name)) return;
    const prevTab = activeTab;
    activeTab = name;
    document.querySelectorAll(".tab").forEach(t => t.classList.toggle("active", t.dataset.session === name));
    document.querySelectorAll(".terminal-wrapper").forEach(w => w.classList.toggle("active", w.id === "term-" + name));
    const entry = terminals.get(name);
    if (entry) {
        if (prevTab !== name) {
            requestAnimationFrame(() => {
                try { entry.fitAddon.fit(); } catch (e) {}
            });
        }
        entry.term.focus();
    }
}

function closeTab(name) {
    const entry = terminals.get(name);
    if (entry) {
        if (entry.ws) entry.ws.close();
        if (entry.ro) entry.ro.disconnect();
        entry.term.dispose();
        entry.wrapper.remove();
        terminals.delete(name);
    }
    fetch(BACKEND + "/api/sessions/" + name, {method: "DELETE"}).catch(() => {});
    const tab = document.querySelector(`.tab[data-session="${name}"]`); if (tab) tab.remove();
    if (activeTab === name) {
        const r = Array.from(terminals.keys());
        if (r.length > 0) switchToTab(r[r.length - 1]);
        else { activeTab = null; emptyState.classList.remove("hidden"); terminalsEl.classList.remove("visible"); }
    }
}

function forceResize(name) {
    const entry = terminals.get(name);
    if (!entry) return;
    try { entry.fitAddon.fit(); } catch (e) {}
}

async function refreshSessions() {
    try {
        const sessions = await api("/api/sessions");
        const alive = new Set((sessions || []).map(s => s.name));
        for (const name of Array.from(terminals.keys())) {
            if (!alive.has(name)) removeTab(name);
        }
        for (const s of (sessions || [])) {
            if (!terminals.has(s.name)) addTab(s.name);
        }
    } catch (err) { addChatMessage("system", "Refresh error: " + err); }
}

function removeTab(name) {
    const entry = terminals.get(name);
    if (entry) {
        if (entry.ro) entry.ro.disconnect();
        entry.term.dispose();
        entry.wrapper.remove();
        terminals.delete(name);
    }
    const tab = document.querySelector(`.tab[data-session="${name}"]`); if (tab) tab.remove();
    if (activeTab === name) {
        const r = Array.from(terminals.keys());
        if (r.length > 0) switchToTab(r[r.length - 1]);
        else { activeTab = null; emptyState.classList.remove("hidden"); terminalsEl.classList.remove("visible"); }
    }
}

function refitAll() { for (const [name] of terminals) forceResize(name); }

btnNew.addEventListener("click", () => {
    const name = prompt("Session name:");
    if (name) fetch(BACKEND + "/api/sessions", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({name})}).then(() => refreshSessions()).catch(err => addChatMessage("system", "Error: " + err));
});
btnRefresh.addEventListener("click", () => refreshSessions());
btnStart.addEventListener("click", () => refreshSessions());

// ── Init ──
fetch(BACKEND + "/api/chat/history").then(r => r.json()).then(msgs => {
    if (msgs && msgs.length > 0) { for (const m of msgs) addChatMessage(m.role, m.content); }
}).catch(() => {});

addChatMessage("system", "Makro GUI ready.");
refreshSessions();
connectChatWs();
setInterval(refreshSessions, 5000);

// ── Autocomplete ──
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
                chatInput.value = "@" + el.dataset.name;
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
chatInput.addEventListener("keydown", (e) => { if (e.key === "Escape") hintEl.classList.remove("visible"); });

// ── Keyboard shortcuts ──
document.addEventListener("keydown", (e) => {
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.key === "b") {
        e.preventDefault();
        chatPanel.classList.toggle("collapsed");
        btnToggle.classList.toggle("collapsed", chatPanel.classList.contains("collapsed"));
        setTimeout(refitAll, 50);
        setTimeout(refitAll, 300);
        if (chatPanel.classList.contains("collapsed")) {
            const entry = activeTab && terminals.get(activeTab);
            if (entry) setTimeout(() => entry.term.focus(), 300);
        }
        return;
    }
    const num = parseInt(e.key);
    if (num >= 1 && num <= 9) {
        e.preventDefault();
        const names = Array.from(terminals.keys());
        if (num <= names.length) switchToTab(names[num - 1]);
        return;
    }
    if (e.key === "l") {
        e.preventDefault();
        const entry = activeTab && terminals.get(activeTab);
        if (entry) entry.term.focus();
    }
});

document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "j") {
        e.preventDefault();
        if (document.activeElement === chatInput) {
            const entry = activeTab && terminals.get(activeTab);
            if (entry) entry.term.focus();
        } else {
            chatInput.focus();
        }
    }
});
