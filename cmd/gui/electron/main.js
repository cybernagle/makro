const { app, BrowserWindow, ipcMain, clipboard, Menu } = require('electron');

// Enable remote debugging for inspection (localhost only, safe).
app.commandLine.appendSwitch('remote-debugging-port', '9222');
const { spawn } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');
const crypto = require('crypto');
const http = require('http');
const https = require('https');

const SERVE_ADDR = '0.0.0.0:7070';

function getCertPaths() {
  const candidates = [
    path.join(process.resourcesPath, 'certs'),
    path.join(__dirname, '..', 'certs'),
  ];
  for (const dir of candidates) {
    const cert = path.join(dir, '127.0.0.1+2.pem');
    const key = path.join(dir, '127.0.0.1+2-key.pem');
    if (fs.existsSync(cert) && fs.existsSync(key)) return { cert, key };
  }
  return null;
}

const CERT_PATHS = getCertPaths();
const USE_TLS = CERT_PATHS !== null;
const PROTO = USE_TLS ? 'https' : 'http';
let win = null;
let makroProcess = null;

function passwordFilePath() {
  return path.join(os.homedir(), '.makro', 'password');
}

function generatePassword() {
  return crypto.randomBytes(16).toString('hex');
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
  if (USE_TLS) {
    args.push('--tls-cert', CERT_PATHS.cert, '--tls-key', CERT_PATHS.key);
  }
  const safeArgs = args.filter((v, i) => args[i - 1] !== '--password' && v !== '--password');
  console.log('[electron] starting backend:', bin, ...safeArgs);
  console.log('[electron] TLS:', USE_TLS ? 'enabled' : 'disabled (no certs found)');

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
  let attempts = 0;
  const check = () => {
    const options = {
      hostname: '127.0.0.1', port: 7070, path: '/api/sessions',
      headers: { 'Authorization': 'Bearer ' + PASSWORD },
      rejectUnauthorized: false,
    };
    const client = USE_TLS ? https : http;
    client.get(options, (res) => {
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
  console.log(`[makro] Server:   ${PROTO}://${localIP}:7070`);
  console.log(`[makro] Password: ${'*'.repeat(8)}`);
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

  // Trust self-signed mkcert certificate for the renderer's fetch/WebSocket.
  if (USE_TLS) {
    win.webContents.session.setCertificateVerifyProc((req, cb) => {
      if (req.hostname === '127.0.0.1' || req.hostname === 'localhost' || req.hostname === localIP) {
        return cb(0);
      }
      cb(-2);
    });
  }

  // Only auto-open devtools in dev mode
  if (process.env.MAKRO_DEV === '1') {
    win.webContents.openDevTools({ mode: 'detach' });
  }

  const isDev = process.env.MAKRO_DEV === '1';
  if (isDev) {
    win.loadURL('http://localhost:5173');
  } else {
    // SERVE_ADDR (0.0.0.0) is the bind address; renderer must load via 127.0.0.1
    // so the cert SAN matches and setCertificateVerifyProc allows it.
    win.loadURL(`${PROTO}://127.0.0.1:7070/`);
  }
}

ipcMain.handle('makro:connectionInfo', () => {
  return { ip: getLocalIP(), port: 7070, password: PASSWORD, useTLS: USE_TLS, proto: PROTO };
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
