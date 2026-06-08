const { app, BrowserWindow, ipcMain, clipboard, Menu } = require('electron');
const { spawn } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');
const crypto = require('crypto');

const SERVE_ADDR = '0.0.0.0:7070';
let win = null;
let makroProcess = null;

function passwordFilePath() {
  return path.join(os.homedir(), '.makro', 'password');
}

function generatePassword() {
  return crypto.randomBytes(4).toString('hex');
}

function loadOrCreatePassword() {
  if (process.env.MAKRO_PASSWORD) return process.env.MAKRO_PASSWORD;
  const file = passwordFilePath();
  try {
    if (fs.existsSync(file)) {
      const saved = fs.readFileSync(file, 'utf8').trim();
      if (saved) return saved;
    }
  } catch (_) { /* fall through to generate */ }
  const generated = generatePassword();
  try {
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, generated, { mode: 0o600 });
  } catch (_) { /* ignore write failure — password still works in-memory */ }
  return generated;
}

const PASSWORD = loadOrCreatePassword();

function getBinPath() {
  const res = path.join(process.resourcesPath, 'bin', 'makro-serve');
  if (require('fs').existsSync(res)) return res;
  return path.join(__dirname, '..', 'bin', 'makro-serve');
}

function getLocalIP() {
  const os = require('os');
  const nets = os.networkInterfaces();
  for (const name of Object.keys(nets)) {
    for (const net of nets[name]) {
      if (net.family === 'IPv4' && !net.internal) {
        return net.address;
      }
    }
  }
  return '127.0.0.1';
}

function startBackend() {
  const bin = getBinPath();
  const args = ['serve', '--addr', SERVE_ADDR, '--password', PASSWORD];
  console.log('[electron] starting backend:', bin, ...args);

  makroProcess = spawn(bin, args, {
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
    const options = { hostname: '127.0.0.1', port: 7070, path: '/api/sessions', headers: { 'Authorization': 'Bearer ' + PASSWORD } };
    http.get(options, (res) => {
      res.resume();
      if (res.statusCode < 500) { cb(); } else { retry(); }
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
  const localIP = getLocalIP();
  console.log(`\n[makro] ─────────────────────────────────────`);
  console.log(`[makro] Server:   http://${localIP}:7070`);
  console.log(`[makro] Password: ${PASSWORD}`);
  console.log(`[makro] ─────────────────────────────────────\n`);

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

  // Only auto-open devtools in dev mode
  if (process.env.MAKRO_DEV === '1') {
    win.webContents.openDevTools({ mode: 'detach' });
  }

  const isDev = process.env.MAKRO_DEV === '1';
  if (isDev) {
    win.loadURL('http://localhost:5173');
  } else {
    win.loadURL('http://' + SERVE_ADDR + '/');
  }
}

ipcMain.handle('makro:connectionInfo', () => {
  return { ip: getLocalIP(), port: 7070, password: PASSWORD };
});

ipcMain.handle('makro:clipboard:read', () => clipboard.readText());
ipcMain.handle('makro:clipboard:write', (_e, text) => clipboard.writeText(String(text || '')));

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
