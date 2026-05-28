const { app, BrowserWindow } = require('electron');
const { spawn } = require('child_process');
const path = require('path');

const SERVE_ADDR = '127.0.0.1:7070';
let win = null;
let makroProcess = null;

function getBinPath() {
  // In packaged app, binary is in Resources/bin/
  const res = path.join(process.resourcesPath, 'bin', 'makro-serve');
  if (require('fs').existsSync(res)) return res;
  // In dev, use the binary from ../../bin/
  return path.join(__dirname, '..', 'bin', 'makro-serve');
}

function startBackend() {
  const bin = getBinPath();
  console.log('[electron] starting backend:', bin, 'serve --addr', SERVE_ADDR);

  makroProcess = spawn(bin, ['serve', '--addr', SERVE_ADDR], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  makroProcess.stdout.on('data', (data) => {
    process.stdout.write(data);
  });

  makroProcess.stderr.on('data', (data) => {
    process.stderr.write(data);
  });

  makroProcess.on('exit', (code) => {
    console.log('[electron] backend exited with code', code);
  });
}

function waitForBackend(cb) {
  const http = require('http');
  let attempts = 0;
  const check = () => {
    http.get(`http://${SERVE_ADDR}/api/sessions`, (res) => {
      if (res.statusCode === 200) { cb(); } else { retry(); }
    }).on('error', () => { retry(); });
  };
  const retry = () => {
    attempts++;
    if (attempts > 50) { console.error('[electron] backend not ready after 10s'); return; }
    setTimeout(check, 200);
  };
  check();
}

function createWindow() {
  win = new BrowserWindow({
    width: 1200,
    height: 800,
    minWidth: 800,
    minHeight: 500,
    titleBarStyle: 'hiddenInset',
    backgroundColor: '#0c0c0e',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
    },
  });

  // In dev, load from Vite dev server; in production, load from Go HTTP server.
  const isDev = process.env.MAKRO_DEV === '1';
  if (isDev) {
    win.loadURL('http://localhost:5173');
  } else {
    win.loadURL('http://' + SERVE_ADDR + '/');
  }
}

app.whenReady().then(() => {
  startBackend();
  waitForBackend(() => {
    console.log('[electron] backend ready');
    createWindow();
  });
});

app.on('window-all-closed', () => {
  if (makroProcess) { makroProcess.kill(); makroProcess = null; }
  app.quit();
});

app.on('before-quit', () => {
  if (makroProcess) { makroProcess.kill(); makroProcess = null; }
});
