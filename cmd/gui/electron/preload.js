const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('makro', {
  backendUrl: 'http://127.0.0.1:7070',
  wsUrl: 'ws://127.0.0.1:7070',
  getConnectionInfo: () => ipcRenderer.invoke('makro:connectionInfo'),
  clipboardRead: () => ipcRenderer.invoke('makro:clipboard:read'),
  clipboardWrite: (text) => ipcRenderer.invoke('makro:clipboard:write', text),
});
