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
import Chart from "chart.js/auto";
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

const appEl = document.getElementById("app");
// Toggle the chat panel: collapses #chat-panel AND the matching toolbar-left
// half (so the terminal tabs slide flush left), keeping them in sync.
function toggleChat() {
    const collapsed = chatPanel.classList.toggle("collapsed");
    btnToggle.classList.toggle("collapsed", collapsed);
    appEl.classList.toggle("chat-collapsed", collapsed);
    return collapsed;
}

btnToggle.addEventListener("click", () => {
    toggleChat();
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
        const collapsed = toggleChat();
        setTimeout(refitAll, 50);
        setTimeout(refitAll, 300);
        if (collapsed) {
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

// Alt+Enter toggles native fullscreen (macOS's native ⌃⌘F aside, Alt+Enter is
// the convention the user expects). Delegated to the main process via IPC.
document.addEventListener("keydown", (e) => {
    if (e.altKey && e.key === "Enter") {
        e.preventDefault();
        window.makro?.toggleFullscreen?.();
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
    renderUsagePanel();
}

// ── Usage (prompt consumption) panel ──
// Time range (hours) and bucket granularity (minutes) are independent, like
// Grafana: pick a window, pick a bucket size. Range also scopes the stat cards.
let usageRange = 24;   // hours: 1 / 5 / 24 / 168 (7d) / 720 (30d)
let usageGran = 60;    // minutes per bucket: 1 / 5 / 15 / 30 / 60
let usageFilter = { session: "", source: "", model: "" };
let usageChartType = "line";       // timeline chart: "line" | "bar"
let usageBreakdownDim = "session"; // doughnut dimension: session | source | model
let usageTlChart = null;           // Chart instance (timeline)
let usageDoughnutChart = null;     // Chart instance (breakdown)
const RANGES = [[1, "1h"], [5, "5h"], [24, "24h"], [168, "7d"], [720, "30d"]];
const GRANS = [[1, "1m"], [5, "5m"], [15, "15m"], [30, "30m"], [60, "1h"]];
// Auto-pick a sensible granularity when the range changes (~30-60 buckets).
const RANGE_DEFAULT_GRAN = { 1: 5, 5: 30, 24: 60, 168: 60, 720: 60 };
// Chart palette (Chart.js needs concrete colors, not CSS vars).
const CHART = {
    accent: "#34d399",
    accentSoft: "rgba(52,211,153,0.18)",
    accentBar: "rgba(52,211,153,0.55)",
    grid: "rgba(255,255,255,0.06)",
    text: "#a1a1aa",
    ticks: "#71717a",
    // doughnut slices
    slices: ["#34d399", "#60a5fa", "#fbbf24", "#f87171", "#c084fc", "#22d3ee", "#a3e635", "#fb923c", "#71717a"],
};

function filterQS() {
    const p = new URLSearchParams();
    if (usageFilter.session) p.set("session", usageFilter.session);
    if (usageFilter.source) p.set("source", usageFilter.source);
    if (usageFilter.model) p.set("model", usageFilter.model);
    const s = p.toString();
    return s ? "&" + s : "";
}

function destroyUsageCharts() {
    if (usageTlChart) { usageTlChart.destroy(); usageTlChart = null; }
    if (usageDoughnutChart) { usageDoughnutChart.destroy(); usageDoughnutChart = null; }
}

async function renderUsagePanel() {
    const panel = document.getElementById("usage-panel");
    if (!panel) return;
    destroyUsageCharts(); // canvases are about to be wiped from the DOM
    panel.innerHTML = '<div class="usage-title">Prompt Usage</div><div class="usage-empty">Loading…</div>';
    try {
        const [stats, diag] = await Promise.all([
            api(`/api/usage/stats?hours=${usageRange}` + filterQS()),
            api("/api/usage/diagnostics"),
        ]);
        panel.innerHTML = usagePanelHTML(stats, diag);
        // Filter dropdowns → re-render the whole panel with the new filter.
        panel.querySelectorAll(".usage-filter").forEach(sel => {
            sel.addEventListener("change", () => {
                usageFilter[sel.dataset.filter] = sel.value;
                renderUsagePanel();
            });
        });
        // Range buttons → set window + auto-granularity, re-render whole panel.
        panel.querySelectorAll(".usage-range-btn").forEach(b => {
            b.addEventListener("click", () => {
                usageRange = Number(b.dataset.range);
                usageGran = RANGE_DEFAULT_GRAN[usageRange] || 60;
                renderUsagePanel();
            });
        });
        // Granularity buttons → re-render just the timeline.
        panel.querySelectorAll(".usage-gran-btn").forEach(b => {
            b.addEventListener("click", () => {
                usageGran = Number(b.dataset.gran);
                panel.querySelectorAll(".usage-gran-btn").forEach(x => x.classList.toggle("active", x === b));
                renderTimeline();
            });
        });
        // Timeline chart type (line/bar) → re-render just the timeline.
        panel.querySelectorAll(".usage-tltype-btn").forEach(b => {
            b.addEventListener("click", () => {
                usageChartType = b.dataset.tltype;
                panel.querySelectorAll(".usage-tltype-btn").forEach(x => x.classList.toggle("active", x === b));
                renderTimeline();
            });
        });
        // Breakdown dimension (session/source/model) → re-render just the doughnut.
        panel.querySelectorAll(".usage-bdim-btn").forEach(b => {
            b.addEventListener("click", () => {
                usageBreakdownDim = b.dataset.bdim;
                panel.querySelectorAll(".usage-bdim-btn").forEach(x => x.classList.toggle("active", x === b));
                renderBreakdownChart(stats);
            });
        });
        const exportBtn = panel.querySelector("#usage-export-btn");
        if (exportBtn) exportBtn.addEventListener("click", exportUsageCsv);
        renderTimeline();
        renderBreakdownChart(stats);
    } catch (e) {
        panel.innerHTML = '<div class="usage-title">Prompt Usage</div><div class="usage-empty">Unavailable</div>';
    }
}

// exportUsageCsv downloads the raw rows (scoped to the current range + filter)
// as a CSV file. Fetches with the auth header, then triggers a blob download.
async function exportUsageCsv() {
    try {
        const r = await fetch(BACKEND + "/api/usage/export?hours=" + usageRange + filterQS(), { headers: authHeaders() });
        if (!r.ok) return;
        const blob = await r.blob();
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = "prompt_usage.csv";
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(url);
    } catch (e) {}
}

function filterSelect(name, allLabel, options, current) {
    let html = `<select class="usage-filter" data-filter="${name}"><option value="">${esc(allLabel)}</option>`;
    for (const o of options) {
        html += `<option value="${esc(o)}"${o === current ? " selected" : ""}>${esc(o)}</option>`;
    }
    return html + `</select>`;
}

function usagePanelHTML(stats, diag) {
    if (!stats) {
        return '<div class="usage-title">Prompt Usage</div><div class="usage-empty">No data yet</div>';
    }
    const stat = (label, val, warn) => {
        const v = typeof val === "string" ? val : fmtNum(val);
        return `<div class="usage-stat ${warn ? "warn" : ""}"><div class="usage-stat-val">${v}</div><div class="usage-stat-label">${label}</div></div>`;
    };

    let html = `<div class="usage-head"><span class="usage-title">Prompt Usage</span>`;
    if (stats.quota_total > 0) {
        const pct = Math.min(100, stats.quota_percent || 0);
        html += `<span class="usage-quota"><span class="usage-quota-bar"><span class="usage-quota-fill" style="width:${pct}%"></span></span><span class="usage-quota-text">${stats.quota_used}/${stats.quota_total} · ${pct.toFixed(0)}%</span></span>`;
    }
    // Range selector (top-right) — scopes stats, breakdowns, and timeline.
    html += `<div class="usage-ranges">`;
    for (const [val, label] of RANGES) {
        html += `<button class="usage-range-btn${val === usageRange ? " active" : ""}" data-range="${val}">${label}</button>`;
    }
    html += `</div><button class="usage-export-btn" id="usage-export-btn" title="Export CSV">⤓ CSV</button></div>`;

    // Filters: session / source / model (options from the unfiltered breakdowns).
    const sessions = stats.by_session ? Object.keys(stats.by_session) : [];
    const models = stats.by_model ? Object.keys(stats.by_model) : [];
    html += `<div class="usage-filters">`;
    html += filterSelect("session", "All sessions", sessions, usageFilter.session);
    html += filterSelect("source", "All sources", ["Claude Code", "Makro"], usageFilter.source);
    html += filterSelect("model", "All models", models, usageFilter.model);
    html += `</div>`;

    // Cache hit rate = cache_read / (input + cache_read + cache_creation).
    const denom = (stats.prompt_tokens || 0) + (stats.cache_read_tokens || 0) + (stats.cache_creation_tokens || 0);
    const hitRate = denom > 0 ? (stats.cache_read_tokens || 0) / denom * 100 : 0;

    html += `<div class="usage-stats">`;
    html += stat("Prompts", stats.total_prompts, false);
    html += stat("Tokens", stats.total_tokens, false);
    html += stat("Cache read", stats.cache_read_tokens || 0, false);
    html += stat("Hit rate", hitRate.toFixed(0) + "%", false);
    html += stat("Input", stats.prompt_tokens, false);
    html += stat("Output", stats.completion_tokens, false);
    html += stat("Duplicates", stats.duplicate_calls, stats.duplicate_calls > 0);
    html += stat("Frequent", stats.frequent_calls, stats.frequent_calls > 0);
    html += `</div>`;

    // Breakdown doughnut — switch dimension (component / session / source / model).
    html += `<div class="usage-bd-head"><span class="usage-tl-title">Breakdown</span><div class="usage-bdims">`;
    for (const d of ["component", "session", "source", "model"]) {
        html += `<button class="usage-bdim-btn${d === usageBreakdownDim ? " active" : ""}" data-bdim="${d}">${d}</button>`;
    }
    html += `</div></div><div class="usage-chart usage-doughnut"><canvas id="usage-doughnut-canvas"></canvas></div>`;

    if (diag && diag.recommendations && diag.recommendations.length) {
        html += `<div class="usage-alerts">`;
        for (const r of diag.recommendations) html += `<div class="usage-alert">⚠ ${esc(r)}</div>`;
        html += `</div>`;
    }

    // Timeline header: title + line/bar toggle + granularity; canvas below.
    html += `<div class="usage-tl-head"><span class="usage-tl-title">Tokens over time</span>`;
    html += `<div class="usage-tltype"><button class="usage-tltype-btn${usageChartType === "line" ? " active" : ""}" data-tltype="line">line</button><button class="usage-tltype-btn${usageChartType === "bar" ? " active" : ""}" data-tltype="bar">bar</button></div>`;
    html += `<div class="usage-gran">`;
    for (const [val, label] of GRANS) {
        html += `<button class="usage-gran-btn${val === usageGran ? " active" : ""}" data-gran="${val}">${label}</button>`;
    }
    html += `</div></div><div class="usage-chart usage-tl"><canvas id="usage-tl-canvas"></canvas></div>`;
    return html;
}

async function renderTimeline() {
    const canvas = document.getElementById("usage-tl-canvas");
    if (!canvas) return;
    const data = await api(`/api/usage/timeline?hours=${usageRange}&granularity=${usageGran}${filterQS()}`);
    if (usageTlChart) { usageTlChart.destroy(); usageTlChart = null; }
    if (!data || !data.length) return;
    const long = usageRange > 48;
    const labels = data.map(p => {
        const hr = p.hour || "";
        return long ? `${hr.slice(5, 10)} ${hr.slice(11, 13)}:00` : hr.slice(11, 16);
    });
    const isLine = usageChartType === "line";
    usageTlChart = new Chart(canvas, {
        type: usageChartType,
        data: {
            labels,
            datasets: [{
                label: "tokens",
                data: data.map(p => p.total_tokens),
                borderColor: CHART.accent,
                backgroundColor: isLine ? CHART.accentSoft : CHART.accentBar,
                borderWidth: 2,
                tension: 0.3,
                fill: isLine,
                pointRadius: isLine ? 0 : undefined,
                pointHoverRadius: isLine ? 3 : undefined,
            }],
        },
        options: {
            responsive: true, maintainAspectRatio: false, animation: false,
            interaction: { mode: "index", intersect: false },
            plugins: {
                legend: { labels: { color: CHART.text, boxWidth: 12, font: { size: 11 } } },
                tooltip: { callbacks: { label: (c) => `${fmtNum(c.parsed.y)} tokens · ${data[c.dataIndex].calls} calls` } },
            },
            scales: {
                x: { ticks: { color: CHART.ticks, maxTicksLimit: 8, autoSkip: true }, grid: { color: CHART.grid } },
                y: { beginAtZero: true, ticks: { color: CHART.ticks, callback: (v) => fmtNum(v) }, grid: { color: CHART.grid }, title: { display: true, text: "tokens", color: CHART.ticks } },
            },
        },
    });
}

// renderBreakdownChart draws a doughnut of token share for the chosen dimension
// (session / source / model), top 8 slices + "other".
function renderBreakdownChart(stats) {
    const canvas = document.getElementById("usage-doughnut-canvas");
    if (!canvas || !stats) return;
    if (usageDoughnutChart) { usageDoughnutChart.destroy(); usageDoughnutChart = null; }
    // "component" is built client-side from the stats scalars (not a GROUP BY).
    const map = usageBreakdownDim === "component"
        ? { cache_read: stats.cache_read_tokens || 0, input: stats.prompt_tokens || 0, output: stats.completion_tokens || 0, ...(stats.cache_creation_tokens ? { cache_create: stats.cache_creation_tokens } : {}) }
        : (stats["by_" + usageBreakdownDim] || {});
    const entries = Object.entries(map)
        .map(([k, v]) => ({ k, t: (typeof v === "number" ? v : (v.total_tokens || 0)) }))
        .sort((a, b) => b.t - a.t);
    if (!entries.length) return;
    const top = entries.slice(0, 8);
    const rest = entries.slice(8).reduce((s, e) => s + e.t, 0);
    if (rest > 0) top.push({ k: "other", t: rest });
    usageDoughnutChart = new Chart(canvas, {
        type: "doughnut",
        data: {
            labels: top.map(e => e.k),
            datasets: [{ data: top.map(e => e.t), backgroundColor: CHART.slices, borderWidth: 0 }],
        },
        options: {
            responsive: true, maintainAspectRatio: false, cutout: "60%",
            plugins: {
                legend: { position: "right", labels: { color: CHART.text, boxWidth: 12, font: { size: 11 } } },
                tooltip: { callbacks: { label: (c) => `${c.label}: ${fmtNum(c.parsed)} tok` } },
            },
        },
    });
}

function fmtNum(n) {
    n = Number(n) || 0;
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
    return String(n);
}

function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, c => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"}[c]));
}

// renderBreakdown emits a labeled chip-row for a {key: {calls, total_tokens}} map.
function renderBreakdown(label, map) {
    if (!map || !Object.keys(map).length) return "";
    const entries = Object.entries(map).sort((a, b) => (b[1].total_tokens || 0) - (a[1].total_tokens || 0));
    let html = `<div class="usage-bd"><span class="usage-bd-label">${esc(label)}</span><div class="usage-models">`;
    for (const [k, ms] of entries) {
        html += `<div class="usage-model"><span class="usage-model-name">${esc(k)}</span><span class="usage-model-meta">${ms.calls} · ${fmtNum(ms.total_tokens)} tok</span></div>`;
    }
    return html + `</div></div>`;
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
