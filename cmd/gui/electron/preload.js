const { contextBridge, ipcRenderer } = require('electron');

// NOTE: sandboxed preload cannot require('path'/'fs'). TLS detection is done in
// main process and surfaced via getConnectionInfo(). Renderer waits for that
// before issuing any fetch/WebSocket.
contextBridge.exposeInMainWorld('makro', {
  getConnectionInfo: () => ipcRenderer.invoke('makro:connectionInfo'),
  clipboardRead: () => ipcRenderer.invoke('makro:clipboard:read'),
  clipboardWrite: (text) => ipcRenderer.invoke('makro:clipboard:write', text),
  toggleFullscreen: () => ipcRenderer.invoke('makro:toggleFullscreen'),
});
