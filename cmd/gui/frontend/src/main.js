// Makro GUI — fetch/WebSocket version (no Wails bindings)
// Backends are configured dynamically via getConnectionInfo() IPC since preload
// is sandboxed and can't probe the filesystem for certs.
let BACKEND = 'http://127.0.0.1:7070';
let WS_URL = 'ws://127.0.0.1:7070';
let PASSWORD = '';
const passwordReady = (window.makro?.getConnectionInfo)
    ? window.makro.getConnectionInfo().then(info => {
        PASSWORD = info.password;
        const proto = info.useTLS ? 'https' : 'http';
        const wsProto = info.useTLS ? 'wss' : 'ws';
        BACKEND = `${proto}://127.0.0.1:${info.port}`;
        WS_URL = `${wsProto}://127.0.0.1:${info.port}`;
    })
    : Promise.resolve();

function authHeaders(extra = {}) {
    const h = {...extra};
    if (PASSWORD) h["Authorization"] = "Bearer " + PASSWORD;
    return h;
}

import {Terminal} from "@xterm/xterm";
import {FitAddon} from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {marked} from "marked";
import hljs from "highlight.js/lib/core";
import js from "highlight.js/lib/languages/javascript";
import ts from "highlight.js/lib/languages/typescript";
import go from "highlight.js/lib/languages/go";
import python from "highlight.js/lib/languages/python";
import bash from "highlight.js/lib/languages/bash";
import json from "highlight.js/lib/languages/json";
import css from "highlight.js/lib/languages/css";
import html from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";
import markdown from "highlight.js/lib/languages/markdown";
import "highlight.js/styles/github-dark-dimmed.css";

hljs.registerLanguage("javascript", js);
hljs.registerLanguage("typescript", ts);
hljs.registerLanguage("go", go);
hljs.registerLanguage("python", python);
hljs.registerLanguage("bash", bash);
hljs.registerLanguage("shell", bash);
hljs.registerLanguage("sh", bash);
hljs.registerLanguage("json", json);
hljs.registerLanguage("css", css);
hljs.registerLanguage("html", html);
hljs.registerLanguage("xml", html);
hljs.registerLanguage("yaml", yaml);
hljs.registerLanguage("yml", yaml);
hljs.registerLanguage("markdown", markdown);

marked.setOptions({
    highlight: (code, lang) => {
        if (lang && hljs.getLanguage(lang)) {
            try { return hljs.highlight(code, {language: lang}).value; } catch (e) {}
        }
        return hljs.highlightAuto(code).value;
    },
});

let activeThinkingEl = null;
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

// ── Connection info popup ──
const btnConnect     = document.getElementById("btn-connect");
const connectPopup   = document.getElementById("connect-popup");
const popupUrl       = document.getElementById("popup-url");
const popupPassword  = document.getElementById("popup-password");
let connectionInfo = null;

if (window.makro?.getConnectionInfo) {
    window.makro.getConnectionInfo().then(info => {
        connectionInfo = info;
        const proto = info.useTLS ? 'https' : 'http';
        if (popupUrl) popupUrl.textContent = `${proto}://${info.ip}:${info.port}`;
        if (popupPassword) popupPassword.textContent = info.password;
    });
}

btnConnect?.addEventListener("click", (e) => {
    e.stopPropagation();
    connectPopup?.classList.toggle("hidden");
});

document.addEventListener("click", (e) => {
    if (!connectPopup?.contains(e.target) && e.target !== btnConnect) {
        connectPopup?.classList.add("hidden");
    }
});

document.querySelectorAll(".popup-copy").forEach(btn => {
    btn.addEventListener("click", () => {
        const field = btn.dataset.copy;
        let text = "";
        if (field === "url" && connectionInfo) {
            const proto = connectionInfo.useTLS ? 'https' : 'http';
            text = `${proto}://${connectionInfo.ip}:${connectionInfo.port}`;
        }
        if (field === "password" && connectionInfo) text = connectionInfo.password;
        navigator.clipboard.writeText(text).then(() => {
            btn.textContent = "Copied!";
            btn.classList.add("copied");
            setTimeout(() => { btn.textContent = "Copy"; btn.classList.remove("copied"); }, 1500);
        });
    });
});

btnToggle.addEventListener("click", () => {
    chatPanel.classList.toggle("collapsed");
    btnToggle.classList.toggle("collapsed", chatPanel.classList.contains("collapsed"));
    setTimeout(refitAll, 250);
});

chatInput.addEventListener("keydown", (e) => {
    if (e.isComposing) return;
    if (e.key === "Enter" && chatInput.value.trim()) { sendChat(chatInput.value); chatInput.value = ""; }
});
btnSend.addEventListener("click", () => {
    if (chatInput.disabled) {
        fetch(BACKEND + "/api/chat/cancel", {method: "POST", headers: authHeaders()}).catch(() => {});
        return;
    }
    if (chatInput.value.trim()) { sendChat(chatInput.value); chatInput.value = ""; }
});

// ── API helpers ──
async function api(path, opts = {}) {
    opts.headers = {...authHeaders(), ...(opts.headers || {})};
    const r = await fetch(BACKEND + path, opts);
    const ct = r.headers.get("content-type") || "";
    if (!r.ok || !ct.includes("application/json")) return null;
    return r.json();
}

// ── Chat ──
function sendChat(text) {
    if (text.trim().startsWith("&")) {
        addChatMessage("user", text, new Date().toISOString());
        fetch(BACKEND + "/api/chat", {method: "POST", headers: authHeaders({"Content-Type": "application/json"}), body: JSON.stringify({text})}).catch(err => addChatMessage("system", "Error: " + err));
        chatInput.disabled = false;
        chatInput.focus();
        return;
    }
    const mention = text.trim().match(/^@(\S+)/);
    if (mention) { switchToTab(mention[1]); }
    addChatMessage("user", text, new Date().toISOString());
    chatInput.disabled = true;
    btnSend.textContent = "Stop";
    activeAssistantEl = addChatMessage("assistant", "", new Date().toISOString());
    currentToolEl = null;
    fetch(BACKEND + "/api/chat", {method: "POST", headers: authHeaders({"Content-Type": "application/json"}), body: JSON.stringify({text})}).catch(err => { appendToEl(activeAssistantEl, "[error: " + err + "]"); chatInput.disabled = false; btnSend.textContent = "Send"; });
}

function connectChatWs() {
    if (chatWs) return;
    chatWs = new WebSocket(WS_URL + "/ws/chat" + (PASSWORD ? "?token=" + PASSWORD : ""));
    chatWs.onmessage = (ev) => {
        try {
            const msg = JSON.parse(ev.data);
            if (msg.type === "ping") return;
            if (msg.type === "user") return;
            if (msg.type === "thinking") {
                if (!activeThinkingEl) {
                    activeThinkingEl = document.createElement("div");
                    activeThinkingEl.className = "thinking-block streaming";
                    const header = document.createElement("div");
                    header.className = "thinking-header";
                    header.innerHTML = '<span class="arrow">▼</span><span class="thinking-badge">thinking</span>';
                    const block = activeThinkingEl;
                    header.addEventListener("click", () => block.classList.toggle("collapsed"));
                    const body = document.createElement("div");
                    body.className = "thinking-body";
                    activeThinkingEl.appendChild(header);
                    activeThinkingEl.appendChild(body);
                    chatMessages.appendChild(activeThinkingEl);
                }
                const body = activeThinkingEl.querySelector(".thinking-body");
                body.textContent += msg.data;
                chatMessages.scrollTop = chatMessages.scrollHeight;
            } else if (msg.type === "assistant") {
                // Collapse thinking block when assistant text starts
                if (activeThinkingEl) { activeThinkingEl.classList.remove("streaming"); activeThinkingEl.classList.add("collapsed"); activeThinkingEl = null; }
                if (currentToolEl) {
                    const newEl = document.createElement("div");
                    newEl.className = "chat-msg assistant";
                    const tsEl = document.createElement("div");
                    tsEl.className = "chat-ts";
                    tsEl.textContent = formatTs(new Date().toISOString());
                    newEl.appendChild(tsEl);
                    const bodyEl = document.createElement("div");
                    bodyEl.className = "chat-body";
                    bodyEl.dataset.raw = "";
                    newEl.appendChild(bodyEl);
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
                if (_renderRAF) { cancelAnimationFrame(_renderRAF); _renderRAF = null; _renderQueue = null; }
                if (activeAssistantEl) {
                    const body = activeAssistantEl.querySelector(".chat-body");
                    if (body) body.innerHTML = marked.parse(body.dataset.raw);
                }
                chatInput.disabled = false; btnSend.textContent = "Send"; chatInput.focus(); activeAssistantEl = null; currentToolEl = null; activeThinkingEl = null;
            } else if (msg.type === "error") {
                appendToEl(activeAssistantEl, "[error: " + msg.data + "]"); chatInput.disabled = false; btnSend.textContent = "Send";
            } else if (msg.type === "system") {
                addChatMessage("system", msg.data);
            } else if (msg.type === "switch_tab") {
                if (msg.data) switchToTab(msg.data);
            } else if (msg.type === "session_state") {
                try {
                    const st = JSON.parse(msg.data);
                    if (st && st.session) setTabState(st.session, st.working, st.unread);
                } catch (e) {}
            }
        } catch (e) {}
    };
    chatWs.onclose = () => { chatWs = null; setTimeout(connectChatWs, 2000); };
    chatWs.onerror = () => { chatWs.close(); };
}

function addChatMessage(role, text, timestamp) {
    const div = document.createElement("div");
    div.className = "chat-msg " + role;
    if (timestamp) {
        const ts = document.createElement("div");
        ts.className = "chat-ts";
        ts.textContent = formatTs(timestamp);
        div.appendChild(ts);
    }
    if (role === "assistant") {
        const body = document.createElement("div");
        body.className = "chat-body";
        body.innerHTML = marked.parse(text || "");
        body.dataset.raw = text || "";
        div.appendChild(body);
    } else if (role === "user") {
        const body = document.createElement("div");
        body.className = "chat-body";
        body.textContent = text;
        div.appendChild(body);
    } else {
        div.textContent = text;
    }
    chatMessages.appendChild(div);
    chatMessages.scrollTop = chatMessages.scrollHeight;
    return div;
}

function formatTs(iso) {
    try {
        const d = new Date(iso);
        const now = new Date();
        const isToday = d.toDateString() === now.toDateString();
        const h = d.getHours().toString().padStart(2, "0");
        const m = d.getMinutes().toString().padStart(2, "0");
        if (isToday) return h + ":" + m;
        return (d.getMonth() + 1) + "/" + d.getDate() + " " + h + ":" + m;
    } catch (e) { return ""; }
}

let _renderRAF = null;
let _renderQueue = null;

function appendToEl(el, text) {
    if (!el) return;
    const body = el.querySelector(".chat-body") || el;
    body.dataset.raw = (body.dataset.raw || "") + text;
    if (!_renderRAF) {
        _renderRAF = requestAnimationFrame(() => {
            _renderRAF = null;
            if (_renderQueue) {
                const { b, e } = _renderQueue;
                b.innerHTML = marked.parse(b.dataset.raw);
                if (activeAssistantEl && e === activeAssistantEl) {
                    const cursor = document.createElement("span");
                    cursor.className = "streaming-cursor";
                    cursor.textContent = "▊";
                    b.appendChild(cursor);
                }
                chatMessages.scrollTop = chatMessages.scrollHeight;
            }
            _renderQueue = null;
        });
    }
    _renderQueue = { b: body, e: el };
}

function addToolCall(toolName) {
    const div = document.createElement("div");
    div.className = "tool-call collapsed";
    const header = document.createElement("div");
    header.className = "tool-call-header";
    header.innerHTML = '<span class="arrow">▼</span><span class="tool-badge">tool</span><span class="tool-name"></span>';
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
    const wsUrl = WS_URL + "/ws/xterm/" + name + "?cols=" + term.cols + "&rows=" + term.rows + (PASSWORD ? "&token=" + PASSWORD : "");
    const ws = new WebSocket(wsUrl);
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
            if (sel) { e.preventDefault(); e.stopPropagation(); (window.makro?.clipboardWrite ?? navigator.clipboard.writeText)(sel).catch(() => {}); }
        } else if (mod && e.key.toLowerCase() === "v") {
            e.preventDefault(); e.stopPropagation();
            const read = window.makro?.clipboardRead ? window.makro.clipboardRead() : navigator.clipboard.readText();
            Promise.resolve(read).then(text => { if (text && ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(text)); }).catch(() => {});
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
    tab.innerHTML = '<span class="tab-dot"></span><span class="tab-idx"></span><span class="name"></span><span class="tab-badge"></span><button class="close">×</button>';
    tab.querySelector(".name").textContent = name;
    updateTabIndices();
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
            // Viewing the tab clears its unread badge locally and server-side
            // (the server then broadcasts the cleared state to other clients).
            clearTabUnread(name);
            fetch(BACKEND + "/api/sessions/" + name + "/viewed", {method: "POST", headers: authHeaders()}).catch(() => {});
        }
        entry.term.focus();
    }
}

// setTabState renders the working pulse + unread badge for a session tab.
// working=true → pulsing dot; unread>0 → red count badge; otherwise idle.
function setTabState(name, working, unread) {
    const tab = document.querySelector(`.tab[data-session="${name}"]`);
    if (!tab) return;
    tab.classList.toggle("working", !!working);
    const badge = tab.querySelector(".tab-badge");
    if (badge) {
        const n = unread || 0;
        badge.textContent = n > 9 ? "9+" : String(n);
        badge.classList.toggle("visible", n > 0);
    }
}

// clearTabUnread drops just the badge locally (working state untouched); used
// on tab switch so the badge vanishes instantly before the WS round-trip.
function clearTabUnread(name) {
    const badge = document.querySelector(`.tab[data-session="${name}"] .tab-badge`);
    if (badge) { badge.textContent = ""; badge.classList.remove("visible"); }
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
    fetch(BACKEND + "/api/sessions/" + name, {method: "DELETE", headers: authHeaders()}).catch(() => {});
    const tab = document.querySelector(`.tab[data-session="${name}"]`); if (tab) tab.remove();
    if (activeTab === name) {
        const r = Array.from(terminals.keys());
        if (r.length > 0) switchToTab(r[r.length - 1]);
        else { activeTab = null; emptyState.classList.remove("hidden"); terminalsEl.classList.remove("visible"); }
    }
    updateTabIndices();
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
            setTabState(s.name, s.working, s.unread);
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

function updateTabIndices() {
    const names = Array.from(terminals.keys());
    document.querySelectorAll(".tab").forEach(t => {
        const idx = names.indexOf(t.dataset.session);
        const idxEl = t.querySelector(".tab-idx");
        if (idxEl && idx >= 0) idxEl.textContent = idx + 1;
    });
}

btnNew.addEventListener("click", () => {
    const name = prompt("Session name:");
    if (name) fetch(BACKEND + "/api/sessions", {method: "POST", headers: authHeaders({"Content-Type": "application/json"}), body: JSON.stringify({name})}).then(() => refreshSessions()).catch(err => addChatMessage("system", "Error: " + err));
});
btnRefresh.addEventListener("click", () => refreshSessions());
btnStart.addEventListener("click", () => refreshSessions());

// ── Init ──
addChatMessage("system", "Makro GUI ready.");
passwordReady.then(() => {
    // Load chat history AFTER password is available, otherwise auth header is empty and 401s.
    fetch(BACKEND + "/api/chat/history", {headers: authHeaders()}).then(r => r.json()).then(msgs => {
        if (msgs && msgs.length > 0) {
            // Separator for restored history
            const sep = document.createElement("div");
            sep.className = "chat-separator";
            chatMessages.appendChild(sep);
            for (const m of msgs) addChatMessage(m.role, m.content, m.timestamp);
            requestAnimationFrame(() => { chatMessages.scrollTop = chatMessages.scrollHeight; });
        }
    }).catch(() => {});

    refreshSessions();
    connectChatWs();
    setInterval(refreshSessions, 5000);
});

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
    if ((e.metaKey || e.ctrlKey) && e.key === ".") {
        e.preventDefault();
        fetch(BACKEND + "/api/chat/cancel", {method: "POST", headers: authHeaders()}).catch(() => {});
        return;
    }
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.key === "t") {
        e.preventDefault();
        if (currentView !== "terminal") switchView("terminal");
        return;
    }
    if (e.key === "d") {
        e.preventDefault();
        if (currentView !== "dashboard") switchView("dashboard");
        return;
    }
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

// ── Dashboard / Kanban ──

let currentView = "terminal";
let tasks = [];
let draggedTaskId = null;

const terminalPanel = document.getElementById("terminal-panel");
const dashboardView = document.getElementById("dashboard-view");
const btnViewTerminal = document.getElementById("btn-view-terminal");
const btnViewDashboard = document.getElementById("btn-view-dashboard");
const dashboardSessions = document.getElementById("dashboard-sessions");
const kanbanBoard = document.getElementById("kanban-board");
const btnAddTask = document.getElementById("btn-add-task");

btnViewTerminal.addEventListener("click", () => switchView("terminal"));
btnViewDashboard.addEventListener("click", () => switchView("dashboard"));

function switchView(view) {
    currentView = view;
    btnViewTerminal.classList.toggle("active", view === "terminal");
    btnViewDashboard.classList.toggle("active", view === "dashboard");
    terminalPanel.classList.toggle("dashboard-active", view === "dashboard");
    tabsEl.style.display = view === "dashboard" ? "none" : "flex";
    if (view === "dashboard") {
        renderDashboard();
    } else {
        refitAll();
    }
}

async function loadTasks() {
    try {
        tasks = await api("/api/tasks") || [];
    } catch (e) { tasks = []; }
}

async function renderDashboard() {
    const sessions = await api("/api/sessions").catch(() => []);
    await loadTasks();
    renderSessionCards(sessions || []);
    renderKanbanBoard();
}

function renderSessionCards(sessions) {
    dashboardSessions.innerHTML = "";
    for (const s of sessions) {
        const card = document.createElement("div");
        card.className = "session-card";
        card.dataset.session = s.name;
        card.innerHTML = '<div class="session-name"></div><div class="session-status"><span class="status-dot"></span><span class="status-text"></span></div>';
        card.querySelector(".session-name").textContent = s.name;
        const dot = card.querySelector(".status-dot");
        const txt = card.querySelector(".status-text");
        if (s.active) {
            dot.classList.add("active");
            txt.textContent = "active";
        } else {
            txt.textContent = "idle";
        }
        // Drop target
        card.addEventListener("dragover", (e) => {
            e.preventDefault();
            e.dataTransfer.dropEffect = "move";
            card.classList.add("drag-over");
        });
        card.addEventListener("dragleave", () => card.classList.remove("drag-over"));
        card.addEventListener("drop", (e) => {
            e.preventDefault();
            card.classList.remove("drag-over");
            const taskId = e.dataTransfer.getData("text/plain");
            if (!taskId) return;
            sendTaskToSession(taskId, s.name);
            card.classList.add("pulse");
            setTimeout(() => card.classList.remove("pulse"), 400);
        });
        // Click to navigate to session
        card.style.cursor = "pointer";
        card.addEventListener("click", () => {
            switchToTab(s.name);
            switchView("terminal");
        });
        dashboardSessions.appendChild(card);
    }
}

function renderKanbanBoard() {
    const columns = { "todo": [], "in-progress": [], "done": [] };
    for (const t of tasks) {
        const col = columns[t.column] || columns["todo"];
        col.push(t);
    }
    for (const [col, colTasks] of Object.entries(columns)) {
        colTasks.sort((a, b) => (a.order || 0) - (b.order || 0));
        const container = kanbanBoard.querySelector(`.kanban-column[data-column="${col}"] .kanban-cards`);
        if (!container) continue;
        container.innerHTML = "";
        for (const t of colTasks) {
            container.appendChild(createKanbanCard(t));
        }
        // Column drop
        const colEl = kanbanBoard.querySelector(`.kanban-column[data-column="${col}"]`);
        colEl.ondragover = (e) => { e.preventDefault(); colEl.classList.add("drag-over"); };
        colEl.ondragleave = (e) => { if (!colEl.contains(e.relatedTarget)) colEl.classList.remove("drag-over"); };
        colEl.ondrop = (e) => {
            e.preventDefault();
            colEl.classList.remove("drag-over");
            const taskId = e.dataTransfer.getData("text/plain");
            if (!taskId) return;
            // Check if dropped on a card within column (reorder)
            const afterEl = getDragAfterElement(container, e.clientY);
            const idx = afterEl ? [...container.children].indexOf(afterEl) : container.children.length;
            updateTask(taskId, { column: col, order: idx });
        };
    }
}

function createKanbanCard(task) {
    const card = document.createElement("div");
    card.className = "kanban-card";
    card.draggable = true;
    card.dataset.taskId = task.id;
    card.innerHTML = '<div class="card-title"></div><div class="card-content"></div><div class="card-actions"><button class="btn-edit" title="Edit">✎</button><button class="btn-delete" title="Delete">×</button></div>';
    card.querySelector(".card-title").textContent = task.title;
    const contentEl = card.querySelector(".card-content");
    contentEl.textContent = task.content || "";
    if (!task.content) contentEl.style.display = "none";

    card.addEventListener("dragstart", (e) => {
        draggedTaskId = task.id;
        e.dataTransfer.setData("text/plain", task.id);
        e.dataTransfer.effectAllowed = "move";
        requestAnimationFrame(() => card.classList.add("dragging"));
    });
    card.addEventListener("dragend", () => {
        card.classList.remove("dragging");
        draggedTaskId = null;
        document.querySelectorAll(".drag-over").forEach(el => el.classList.remove("drag-over"));
    });

    card.querySelector(".btn-edit").addEventListener("click", (e) => { e.stopPropagation(); openTaskModal(task); });
    card.querySelector(".btn-delete").addEventListener("click", (e) => { e.stopPropagation(); deleteTask(task.id); });
    card.addEventListener("dblclick", () => openTaskModal(task));

    return card;
}

function getDragAfterElement(container, y) {
    const els = [...container.querySelectorAll(".kanban-card:not(.dragging)")];
    return els.reduce((closest, child) => {
        const box = child.getBoundingClientRect();
        const offset = y - box.top - box.height / 2;
        if (offset < 0 && offset > closest.offset) {
            return { offset, element: child };
        }
        return closest;
    }, { offset: Number.NEGATIVE_INFINITY }).element;
}

async function createTask(title, content) {
    await fetch(BACKEND + "/api/tasks", { method: "POST", headers: authHeaders({ "Content-Type": "application/json" }), body: JSON.stringify({ title, content }) });
    renderDashboard();
}

async function updateTask(id, patch) {
    await fetch(BACKEND + "/api/tasks/" + id, { method: "PUT", headers: authHeaders({ "Content-Type": "application/json" }), body: JSON.stringify(patch) });
    renderDashboard();
}

async function deleteTask(id) {
    await fetch(BACKEND + "/api/tasks/" + id, { method: "DELETE", headers: authHeaders() });
    renderDashboard();
}

async function sendTaskToSession(taskId, sessionName) {
    await fetch(BACKEND + "/api/tasks/" + taskId + "/send", { method: "POST", headers: authHeaders({ "Content-Type": "application/json" }), body: JSON.stringify({ session: sessionName }) });
    addChatMessage("system", `Sent task to @${sessionName}`);
    renderDashboard();
}

function openTaskModal(task) {
    const overlay = document.createElement("div");
    overlay.className = "task-modal-overlay";
    const isEdit = !!task;
    overlay.innerHTML = `<div class="task-modal"><input class="modal-title" placeholder="Task title" value="${isEdit ? task.title.replace(/"/g, "&quot;") : ""}"/><textarea class="modal-content" placeholder="Content to send to session">${isEdit ? (task.content || "") : ""}</textarea><div class="modal-actions"><button class="btn-cancel">Cancel</button><button class="btn-save">${isEdit ? "Save" : "Create"}</button></div></div>`;
    const titleInput = overlay.querySelector(".modal-title");
    const contentInput = overlay.querySelector(".modal-content");
    overlay.querySelector(".btn-cancel").addEventListener("click", () => overlay.remove());
    overlay.querySelector(".btn-save").addEventListener("click", () => {
        const title = titleInput.value.trim();
        const content = contentInput.value.trim();
        if (!title) return;
        if (isEdit) {
            updateTask(task.id, { title, content });
        } else {
            createTask(title, content);
        }
        overlay.remove();
    });
    overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
    document.body.appendChild(overlay);
    titleInput.focus();
}

btnAddTask.addEventListener("click", () => openTaskModal(null));

// Refresh dashboard when session list refreshes
const _origRefresh = refreshSessions;
refreshSessions = async function() {
    await _origRefresh();
    if (currentView === "dashboard") renderDashboard();
};
