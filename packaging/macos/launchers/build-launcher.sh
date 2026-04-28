#!/usr/bin/env bash
# build-launcher.sh -- scaffold a macOS .app bundle that launches an
# upstream client through clyde's MITM proxy.
#
# Usage:
#   build-launcher.sh <upstream> <output-app-path>
#
# Example:
#   build-launcher.sh codex-desktop "$HOME/Applications/Codex (via clyde).app"
#
# The bundle's MacOS executable runs `clyde mitm launch <upstream>`
# which ensures the proxy is up, spawns the upstream binary with
# LaunchProfile env + Chromium flags, and detaches. The wrapper
# returns and the upstream owns its own session.
#
# Pin the resulting .app to the dock; clicking the icon routes the
# upstream through the MITM proxy automatically.
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: build-launcher.sh <upstream> <output-app-path>" >&2
  exit 1
fi

UPSTREAM="$1"
OUT_APP="$2"

CLYDE_BIN="${CLYDE_BIN:-$HOME/.local/bin/clyde}"
if [[ ! -x "$CLYDE_BIN" ]]; then
  CLYDE_BIN="$(command -v clyde || true)"
fi
if [[ -z "$CLYDE_BIN" || ! -x "$CLYDE_BIN" ]]; then
  echo "error: clyde binary not found (set CLYDE_BIN or install via make install)" >&2
  exit 1
fi

case "$UPSTREAM" in
  codex-desktop)
    DISPLAY_NAME="Codex (via clyde)"
    BUNDLE_ID="io.goodkind.clyde.launcher.codex-desktop"
    ICON_SOURCE="/Applications/Codex.app/Contents/Resources/AppIcon.icns"
    ;;
  claude-desktop)
    DISPLAY_NAME="Claude (via clyde)"
    BUNDLE_ID="io.goodkind.clyde.launcher.claude-desktop"
    ICON_SOURCE="/Applications/Claude.app/Contents/Resources/AppIcon.icns"
    ;;
  vscode)
    DISPLAY_NAME="VS Code (via clyde)"
    BUNDLE_ID="io.goodkind.clyde.launcher.vscode"
    ICON_SOURCE="/Applications/Visual Studio Code.app/Contents/Resources/Code.icns"
    ;;
  *)
    echo "error: unsupported upstream $UPSTREAM (only Electron upstreams are dock-pinnable)" >&2
    exit 1
    ;;
esac

# Wipe any prior bundle so re-running this script is idempotent.
rm -rf "$OUT_APP"
mkdir -p "$OUT_APP/Contents/MacOS"
mkdir -p "$OUT_APP/Contents/Resources"

# Copy the upstream's icon when available so the dock entry is
# visually identifiable.
if [[ -f "$ICON_SOURCE" ]]; then
  cp "$ICON_SOURCE" "$OUT_APP/Contents/Resources/AppIcon.icns"
fi

cat >"$OUT_APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>${DISPLAY_NAME}</string>
  <key>CFBundleDisplayName</key>
  <string>${DISPLAY_NAME}</string>
  <key>CFBundleIdentifier</key>
  <string>${BUNDLE_ID}</string>
  <key>CFBundleVersion</key>
  <string>1.0</string>
  <key>CFBundleShortVersionString</key>
  <string>1.0</string>
  <key>CFBundleExecutable</key>
  <string>launcher</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>LSUIElement</key>
  <true/>
</dict>
</plist>
EOF

cat >"$OUT_APP/Contents/MacOS/launcher" <<EOF
#!/usr/bin/env bash
# Wrapper that ensures the clyde MITM proxy is up and spawns the
# real ${UPSTREAM} client with the right env + Chromium flags. The
# child runs detached; this wrapper returns immediately.
LOG_DIR="\${XDG_STATE_HOME:-\$HOME/.local/state}/clyde/mitm-launcher"
mkdir -p "\$LOG_DIR"
exec "${CLYDE_BIN}" mitm launch --upstream "${UPSTREAM}" \\
  >>"\$LOG_DIR/${UPSTREAM}.log" 2>&1
EOF
chmod +x "$OUT_APP/Contents/MacOS/launcher"

echo "wrote launcher app: $OUT_APP"
echo "drag it to your Dock (Finder -> drag the .app to the Dock divider)"
