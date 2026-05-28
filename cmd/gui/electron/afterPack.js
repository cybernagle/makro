const { execSync } = require('child_process');
exports.default = async function (context) {
  const appDir = context.appOutDir;
  console.log('[afterPack] stripping xattrs from', appDir);
  try {
    execSync(`find "${appDir}" -type f -exec xattr -cr {} \\;`, { stdio: 'inherit' });
  } catch (e) {}
};
