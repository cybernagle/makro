const { contextBridge } = require('electron');

contextBridge.exposeInMainWorld('makro', {
  backendUrl: 'http://127.0.0.1:7070',
  wsUrl: 'ws://127.0.0.1:7070',
});
