# IPTV Proxy with Failover Pool, Category Filtering & NordVPN SOCKS5

A high-performance IPTV proxy for **Xtream Codes** and **M3U** playlists with built-in multi-provider failover, category filtering (Live, VOD, Series), in-memory caching, NordVPN SOCKS5 proxy support, and a modern Web Admin UI.

---

## 🌟 Key Features

* **Live Stream Hub & Multiplexing**: Multiple clients watching the same live channel share a single upstream connection to your IPTV provider, saving provider connection limits and VPS bandwidth. Features 188-byte MPEG-TS packet boundary alignment, a 20-second silence watchdog, and automatic exponential backoff stream reconnection.
* **Multi-User Accounts & Connection Limits**: Create individual player credentials directly from the Web Admin UI. Enforce maximum concurrent connection limits per user with smart channel-switch rollover (old stream slot instantly freed for same IP) and `403 Forbidden` blocking for unauthorized concurrent IPs.
* **Xtream Codes & M3U Compatible**: Works with Televizo, TiviMate, IPTnator, IPTV Smarters, VLC, and any player supporting Xtream Codes API or M3U playlists.
* **NordVPN SOCKS5 Proxy**: Routes all upstream traffic through NordVPN SOCKS5 servers to bypass ISP blocks, throttling, and geo-restrictions without installing VPN software on the host.
* **Dynamic Category Filtering**: Selectively include or exclude Live TV, Movies (VOD), and Series categories from the Web UI. Filtered categories are blocked from both category lists and bulk searches.
* **Multi-Provider Failover Pool**: Aggregate multiple IPTV providers or backup server URLs into a single playlist with automatic health testing and failover.
* **Persistent Storage**: Configuration files (`provider.json`, `filters.json`, and `users.json`) are stored in a dedicated host-mounted volume that survives container restarts, updates, and rebuilds.
* **Modern Web Admin Dashboard**: Manage providers, failover servers, user accounts, active live streams, and category filters from an intuitive web interface.
* **Optimized Stream Proxying**: High-performance HTTP client with connection pooling, keep-alive reuse, and metadata caching for instant playlist loading.

---

## 🚀 Quick Start on a VPS (Debian / Ubuntu)

## 🚀 Quick Start on a VPS (Zero-Config Setup)

You do **not** need to create or edit any YAML or configuration files to get started! You can boot the proxy with a single command and configure all providers, users, and category filters directly from the responsive Web Admin interface.

### Step 1: Install Docker on your VPS (Debian / Ubuntu)
```bash
apt update && apt upgrade -y
apt install -y docker.io docker-compose curl git jq
systemctl enable --now docker
```

### Step 2: Clone and Start
```bash
git clone https://github.com/chernandezweb/iptv-proxy.git ~/iptv-proxy
cd ~/iptv-proxy
docker compose up -d --build
```

### Step 3: Open the Web Admin Dashboard
Open your browser (on desktop or mobile) and go to:
```text
http://<YOUR_VPS_IP>:8080/admin/
```
* **Default Username**: `admin`
* **Default Password**: `admin`

That's it! From the Web Admin UI, you can:
1. **Add Your IPTV Provider(s)**: Go to the **Providers** tab and click **➕ Add Your First Provider** to enter your Xtream URL, username, password, and optional backup failover URLs.
2. **Set Up User Accounts**: Go to the **Users** tab to change the default admin password and create separate accounts for family members or devices with custom concurrent stream limits.
3. **Filter Categories**: Go to the **Live TV**, **Movies**, or **Series** tabs to uncheck unwanted or adult categories.

All configurations are automatically saved to `./data/` on your host and persist permanently across container restarts, updates, and rebuilds.

---

## ⚙️ Advanced Configuration (Optional)

If you prefer headless environment variables or want to route upstream traffic through **NordVPN SOCKS5**, you can customize `docker-compose.yml`:

```yaml
version: "3.8"

services:
  iptv-proxy:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: iptv-proxy
    restart: unless-stopped
    ports:
      # Format: HOST_PORT:CONTAINER_PORT
      - "8080:8080"
    volumes:
      # Persistent storage for provider.json, users.json, and filters.json
      - ./data:/data
    environment:
      PORT: 8080
      DATA_DIR: "/data"
      GIN_MODE: release

      # Default admin credentials
      USER: "admin"
      PASSWORD: "admin"

      # Optional: Hardcode upstream provider (or configure via Web UI)
      # XTREAM_BASE_URL: "http://provider-domain.com:8080"
      # XTREAM_USER: "upstream_username"
      # XTREAM_PASSWORD: "upstream_password"

      # Optional: Route upstream through NordVPN SOCKS5 (see below)
      # ALL_PROXY: "socks5://NORD_USER:NORD_PASS@se.socks.nordhold.net:1080"
      # HTTP_PROXY: "socks5://NORD_USER:NORD_PASS@se.socks.nordhold.net:1080"
      # HTTPS_PROXY: "socks5://NORD_USER:NORD_PASS@se.socks.nordhold.net:1080"
```

---

## 🛡️ NordVPN SOCKS5 Setup (Optional)

Using NordVPN SOCKS5 hides your VPS IP address from your IPTV provider and avoids ISP blocks or throttling without installing VPN software on the host.

### 1. Get your NordVPN Service Credentials
> **Important:** SOCKS5 does **NOT** use your standard NordVPN email/password. You must generate **Service Credentials**:
1. Log in to your [Nord Account Dashboard](https://my.nordaccount.com/).
2. Navigate to **Services** → **NordVPN** → **Manual setup** (or **Service credentials**).
3. Copy your generated **Username** and **Password**.

### 2. Available NordVPN SOCKS5 Servers
Choose a server location closest to your IPTV provider or VPS:
* `se.socks.nordhold.net:1080` (Sweden)
* `nl.socks.nordhold.net:1080` (Netherlands)
* `us.socks.nordhold.net:1080` (United States)
* `de.socks.nordhold.net:1080` (Germany)
* `ie.socks.nordhold.net:1080` (Ireland)

Format for `docker-compose.yml`:
```text
socks5://<NORD_SERVICE_USERNAME>:<NORD_SERVICE_PASSWORD>@<SERVER_HOSTNAME>:1080
```

---

## 🏃 Running and Managing the Proxy

### Start the Proxy
```bash
cd ~/iptv-proxy
docker compose up -d --build
```

### View Live Logs
```bash
docker compose logs -f iptv-proxy
```
You should see:
```text
[iptv-proxy] Storage directory configured: /data (filters: /data/filters.json, provider: /data/provider.json)
[iptv-proxy] Loaded filters successfully from /data/filters.json
```

### Stop or Restart
```bash
# Restart
docker compose restart iptv-proxy

# Stop
docker compose down

# Rebuild after git update
git pull
docker compose up -d --build
```

---

## 🖥️ Web Admin Dashboard

Open your browser and navigate to:
```text
http://<YOUR_VPS_IP>:8060/admin/
```

* **Username**: The `USER` set in `docker-compose.yml`
* **Password**: The `PASSWORD` set in `docker-compose.yml`

### What You Can Do in the Admin UI:
1. **Providers Tab**:
   * Add multiple IPTV providers to aggregate into a single playlist.
   * Add backup/failover server URLs for each provider.
   * Run latency and connectivity tests (`🧪 Test Server`).
2. **Category Filtering Tabs (Live TV, Movies, Series)**:
   * Search and uncheck unwanted categories (e.g. adult categories, foreign languages, unneeded sports packages).
   * Click **💾 Save Filters**. Caches are cleared instantly and new filters take effect immediately.
3. **Data Persistence**:
   * All saved filters and provider configurations are written directly to `./data/filters.json` and `./data/provider.json` on your host. They survive all future container rebuilds.

---

## 📺 Configuring Your IPTV Player

Connect your IPTV apps (Televizo, TiviMate, IPTnator, IPTV Smarters, XCIPTV, etc.) using the proxy's credentials:

### 1. Xtream Codes API (Recommended)
| Field | Value |
| :--- | :--- |
| **Server URL** | `http://<YOUR_VPS_IP>:8080` |
| **Username** | Your username (configured in Web Admin -> Users tab) |
| **Password** | Your password (configured in Web Admin -> Users tab) |

> **Tip for Televizo / TiviMate:** If you update category filters in the Web Admin, go to **Settings → Playlists → [Your Playlist] → Reload / Update Playlist** in your player app to flush its local database and load the new filtered catalog.

### 2. M3U Playlist & EPG URLs
* **M3U Playlist**:
  ```text
  http://<YOUR_VPS_IP>:8080/iptv.m3u?username=YOUR_USER&password=YOUR_PASSWORD
  ```
* **EPG (XMLTV Guide)**:
  ```text
  http://<YOUR_VPS_IP>:8080/xmltv.php?username=YOUR_USER&password=YOUR_PASSWORD
  ```

---

## 🔍 Troubleshooting & FAQ

### 1. Why are changes lost when restarting the container?
Ensure your `docker-compose.yml` has the `volumes` section enabled:
```yaml
volumes:
  - ./data:/data
```
Check that the folder exists on your VPS:
```bash
ls -la ~/iptv-proxy/data/
# Should contain: filters.json  provider.json  users.json
```

### 2. How can I verify NordVPN SOCKS5 is working?
Run this command from the host or check proxy logs:
```bash
docker exec -it iptv-proxy sh -c "echo ALL_PROXY=\$ALL_PROXY"
```
When active, requests to your IPTV provider originate from NordVPN's IP rather than your VPS IP.

### 3. Upstream Provider Returns 403 Forbidden
Some IPTV providers block standard HTTP clients or specific User-Agents. Set `USER_AGENT: "IPTVSmartersPro"` or `"TiviMate/4.7.0 (Android TV)"` in `docker-compose.yml`.

### 4. Adult movies still appear in search after unchecking the category?
1. Ensure the container has the latest updates.
2. In your IPTV player app (such as Televizo), tap **Reload Playlist** or clear playlist cache so the player updates its local search index.

---

## 📄 License

GPL-3.0 License. See `LICENSE` for details.
