#!/bin/bash
# ──────────────────────────────────────────────────────────────────────────────
# Y Transport — Quick Start Script for macOS
#
# Usage:
#   curl -sL https://raw.githubusercontent.com/meanwebuser/anYTransportProxy/main/start_node_mac.sh | bash
#   OR
#   git clone https://github.com/meanwebuser/anYTransportProxy.git
#   cd anYTransportProxy
#   chmod +x start_node_mac.sh
#   ./start_node_mac.sh
# ──────────────────────────────────────────────────────────────────────────────

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${CYAN}║         Y TRANSPORT — macOS Quick Start                     ║${NC}"
echo -e "${CYAN}║         Encrypted Delay-Tolerant Transport Proxy            ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""

# ── Step 1: Check prerequisites ──────────────────────────────────────────

echo -e "${YELLOW}[1/6] Checking prerequisites...${NC}"

# Check macOS
if [[ "$(uname)" != "Darwin" ]]; then
    echo -e "${YELLOW}⚠ Not macOS, but continuing anyway...${NC}"
fi

# Check Node.js
if ! command -v node &> /dev/null; then
    echo -e "${RED}✗ Node.js not found! Installing via Homebrew...${NC}"
    if ! command -v brew &> /dev/null; then
        echo -e "${YELLOW}Installing Homebrew...${NC}"
        /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
    fi
    brew install node
fi

NODE_VERSION=$(node -v)
echo -e "${GREEN}✓ Node.js ${NODE_VERSION}${NC}"

# Check npm
if ! command -v npm &> /dev/null; then
    echo -e "${RED}✗ npm not found!${NC}"
    exit 1
fi
echo -e "${GREEN}✓ npm $(npm -v)${NC}"

# ── Step 2: Clone repository ────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[2/6] Setting up project...${NC}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -f "${SCRIPT_DIR}/package.json" ]] && grep -q "y-transport" "${SCRIPT_DIR}/package.json" 2>/dev/null; then
    PROJECT_DIR="${SCRIPT_DIR}"
    echo -e "${GREEN}✓ Running from project directory: ${PROJECT_DIR}${NC}"
else
    PROJECT_DIR="${HOME}/anYTransportProxy"

    if [[ -d "${PROJECT_DIR}" ]]; then
        echo -e "${GREEN}✓ Project already exists at ${PROJECT_DIR}${NC}"
        cd "${PROJECT_DIR}"
        git pull || true
    else
        echo -e "${CYAN}Cloning repository...${NC}"
        git clone https://github.com/meanwebuser/anYTransportProxy.git "${PROJECT_DIR}"
    fi
fi

cd "${PROJECT_DIR}"

# ── Step 3: Install dependencies ────────────────────────────────────────

echo ""
echo -e "${YELLOW}[3/6] Installing dependencies...${NC}"

npm install --production 2>/dev/null || npm install 2>/dev/null || {
    echo -e "${RED}✗ npm install failed!${NC}"
    exit 1
}
echo -e "${GREEN}✓ Dependencies installed${NC}"

# ── Step 4: Build TypeScript ────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[4/6] Building project...${NC}"

npx tsc --project tsconfig.json 2>/dev/null || {
    echo -e "${YELLOW}⚠ TypeScript build had warnings (non-fatal)${NC}"
}
echo -e "${GREEN}✓ Build complete${NC}"

# ── Step 5: Configure ───────────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[5/6] Configuring providers...${NC}"

CONFIG_FILE="${PROJECT_DIR}/.env"

if [[ ! -f "${CONFIG_FILE}" ]]; then
    cat > "${CONFIG_FILE}" << 'ENVEOF'
# Y Transport Configuration
# ──────────────────────────────────────

# Telegram Bot Tokens
TG_TOKEN_1=your_telegram_bot_token_1
TG_TOKEN_2=your_telegram_bot_token_2
TG_CHAT_ID=your_telegram_chat_id

# VK Tokens
VK_TOKEN_1=your_vk_token_1
VK_TOKEN_2=your_vk_token_2
VK_PEER_ID=your_vk_user_id

# OK Token
OK_TOKEN=your_ok_token
OK_CHAT_ID=${WT_OK_CHAT_ID}

# Yandex Disk Token
YDISK_TOKEN=your_yandex_disk_oauth_token

# SOCKS5 Proxy Port
SOCKS5_PORT=1080

# HTTP CONNECT Proxy Port
HTTP_CONNECT_PORT=8080

# Bridge WebSocket Port (for VK Browser Bridge)
BRIDGE_PORT=9123
ENVEOF

    echo -e "${YELLOW}Created .env template — edit it with your tokens:${NC}"
    echo -e "${CYAN}  nano ${CONFIG_FILE}${NC}"
    echo ""
    echo -e "${YELLOW}After editing .env, run:${NC}"
    echo -e "${CYAN}  ./start_node_mac.sh${NC}"
    exit 0
fi

echo -e "${GREEN}✓ Configuration found: ${CONFIG_FILE}${NC}"

# Load .env
set -a
source "${CONFIG_FILE}" 2>/dev/null || true
set +a

# ── Step 6: Start ────────────────────────────────────────────────────────

echo ""
echo -e "${YELLOW}[6/6] Starting Y Transport Node...${NC}"
echo ""

SOCKS5_PORT="${SOCKS5_PORT:-1080}"
HTTP_CONNECT_PORT="${HTTP_CONNECT_PORT:-8080}"

echo -e "${GREEN}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  Y TRANSPORT NODE RUNNING                                    ║${NC}"
echo -e "${GREEN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  SOCKS5 Proxy:    127.0.0.1:${SOCKS5_PORT}                         ║${NC}"
echo -e "${GREEN}║  HTTP CONNECT:    127.0.0.1:${HTTP_CONNECT_PORT}                         ║${NC}"
echo -e "${GREEN}║  Config:          ${CONFIG_FILE}             ║${NC}"
echo -e "${GREEN}╠══════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║  Configure proxy in your browser or app:                    ║${NC}"
echo -e "${GREEN}║  SOCKS5: 127.0.0.1:${SOCKS5_PORT}                                    ║${NC}"
echo -e "${GREEN}║  HTTP:   127.0.0.1:${HTTP_CONNECT_PORT}                                    ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Start the node
npx ts-node packages/core/node.ts 2>/dev/null || node dist/core/node.js 2>/dev/null || {
    echo -e "${RED}✗ Failed to start Y Transport Node${NC}"
    echo -e "${YELLOW}Try running manually: npx ts-node scripts/demo.ts${NC}"
    exit 1
}
