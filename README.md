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

## 🚀 Quick Start on a VPS (Zero-Config Setup)

You do **not** need to create or edit any YAML or configuration files to get started! You can boot the proxy with a single command and configure all providers, users, and category filters directly from the responsive Web Admin interface.

### Step 1: Install Docker on your VPS (Debian / Ubuntu)
```bash
apt update && apt upgrade -y
apt install -y docker.io docker-compose curl git jq ufw
systemctl enable --now docker
```

### Step 2: Configure Firewall & Open Ports
Make sure port `8080` (and `22` for SSH) are open so your IPTV apps and browser can connect:

```bash
# 1. Allow SSH first so you don't get locked out of your VPS!
ufw allow 22/tcp

# 2. Open port 8080 for the IPTV Proxy & Web Admin
ufw allow 8080/tcp

# 3. Enable UFW firewall
ufw --force enable
ufw status
```

> [!IMPORTANT]
> **Cloud Provider Firewalls (Oracle Cloud, AWS EC2, GCP, Hetzner, OVH, DigitalOcean):**
> In addition to the VPS command line, you **must** allow TCP ingress on port `8080` in your Cloud Console's **Security Groups** or **Security Lists** (Source CIDR: `0.0.0.0/0` -> Destination Port: `8080`).
>
> **Oracle Cloud Ubuntu Users:** Oracle instances have an extra `iptables` rule that blocks ports by default even when UFW is open. Run:
> ```bash
> iptables -I INPUT 6 -m state --state NEW -p tcp --dport 8080 -j ACCEPT
> netfilter-persistent save 2>/dev/null || true
> ```

### Step 3: Clone and Start
```bash
git clone https://github.com/chernandezweb/iptv-proxy.git ~/iptv-proxy
cd ~/iptv-proxy
docker compose up -d --build
```

### Step 4: Open the Web Admin Dashboard
Open your browser (on desktop or mobile) and go to:
```text
http://<YOUR_VPS_IP>:8080/admin/
```
* **Default Username**: `admin`
* **Default Password**: `admin`

That's it! Everything can be configured directly from the Web Admin:
1. **Providers Tab**: Click **➕ Add Your First Provider** to enter your Xtream URL, username, password, and optional backup failover URLs.
2. **Users Tab**: Change the default admin password and create player accounts for family members or devices with concurrent connection limits.
3. **VPN / Proxy Tab**: Turn on NordVPN SOCKS5 with one click, choose a server preset (Sweden, Netherlands, US, Germany, etc.), test your outbound IP with `🧪 Test Proxy`, and save.
4. **Live Hub Tab**: Monitor multiplexed streams in real time (multiple devices watching the same live channel share 1 upstream provider connection).
5. **Filtering Tabs**: Check or uncheck Live TV, Movie (VOD), and Series categories to hide adult content or unwanted channels.

All configurations are automatically saved to `./data/` on your host and persist permanently across container restarts, updates, and rebuilds.

---

## 🛡️ Upstream VPN / SOCKS5 Setup (NordVPN Recommended & Multi-VPN Support)

Routing outbound IPTV traffic through a SOCKS5 or HTTP proxy hides your VPS IP address from your IPTV providers, avoids ISP throttling/blocks, and bypasses regional geo-restrictions—without having to route the entire VPS network through a VPN.

> **⭐ Recommendation:** **NordVPN** is the recommended choice due to its high-bandwidth SOCKS5 infrastructure in Sweden, Netherlands, and USA, providing seamless zero-buffering 1080p/4K IPTV streaming. However, **any standard SOCKS5 or HTTP proxy works**.

---

### Supported VPN & Proxy Providers Quick Reference

| Provider | Protocol | Host / Endpoint | Port | Credentials Needed |
| :--- | :--- | :--- | :--- | :--- |
| **⭐ NordVPN (Recommended)** | SOCKS5 | `se.socks.nordhold.net` *(or `nl`, `us`, `de`)* | `1080` | Unique Service Credentials *(from Nord Dashboard)* |
| **Private Internet Access (PIA)** | SOCKS5 | `proxy-nl.privateinternetaccess.com` | `1080` | Generated SOCKS credentials *(starts with `x`)* |
| **TorGuard** | SOCKS5 | `proxy.torguard.org` | `1080` | TorGuard Proxy credentials |
| **IPVanish** | SOCKS5 | `socks.ipvanish.com` | `1080` | IPVanish account credentials |
| **Windscribe** | SOCKS5 | `socks5.windscribe.com` | `1080` | Windscribe SOCKS5 credentials |
| **Gluetun Sidecar** *(Mullvad, Proton, WireGuard)* | SOCKS5 | `gluetun` *(Docker container)* | `1080` | None *(handled by container)* |
| **Custom / Self-Hosted** *(Dante, Squid, Shadowsocks)* | SOCKS5 / HTTP | `<YOUR_SERVER_IP>` | `1080` / `8080` | Username / Password *(if enabled)* |

---

### Step-by-Step Configuration Guides

#### 1. ⭐ NordVPN (Recommended)
1. Log in to your [Nord Account Dashboard](https://my.nordaccount.com/).
2. Navigate to **Services** → **NordVPN** → **Manual Setup** (or **Service Credentials**).
3. Copy your generated **Username** and **Password** *(these are different from your login email)*.
4. In the IPTV Proxy Web Admin (**🛡️ VPN / Proxy** tab), select the **NordVPN - Sweden** preset.
5. Paste your service credentials, click **🧪 Test Proxy & Check IP**, and click **💾 Save & Apply Proxy**.

#### 2. Private Internet Access (PIA)
1. Log in to the [PIA Client Control Panel](https://www.privateinternetaccess.com/pages/client-sign-in).
2. Scroll to **Generate PPTP/L2TP/SOCKS Username and Password** and click **Generate**.
3. In the Web Admin, select the **Private Internet Access (PIA) - Netherlands** preset (`proxy-nl.privateinternetaccess.com:1080`).
4. Enter your generated `x` username and password, test, and save.

#### 3. TorGuard
1. Log into your [TorGuard Client Area](https://torguard.net/clientarea.php).
2. Go to **My Services** → select your proxy package → **Manage Credentials**.
3. Set your Proxy Username and Password.
4. In the Web Admin, select the **TorGuard - SOCKS5** preset (`proxy.torguard.org:1080`) and save.

#### 4. IPVanish
1. SOCKS5 access is included with your IPVanish subscription.
2. In the Web Admin, select **IPVanish - SOCKS5** (`socks.ipvanish.com:1080`).
3. Enter your standard IPVanish account username and password and save.

#### 5. Windscribe
1. Log in to your [Windscribe Account](https://windscribe.com/myaccount).
2. Navigate to **Account Settings** → **SOCKS5 Credentials** to view your credentials.
3. In the Web Admin, select **Windscribe - SOCKS5** (`socks5.windscribe.com:1080`) and enter your credentials.

#### 6. WireGuard / OpenVPN via Gluetun Sidecar (Mullvad, ProtonVPN, ExpressVPN)
If your VPN provider only offers `.conf` or WireGuard configs (such as Mullvad or ProtonVPN), run [Gluetun](https://github.com/qdm12/gluetun) in your `docker-compose.yml`:

```yaml
services:
  gluetun:
    image: qmcgaw/gluetun
    cap_add:
      - NET_ADMIN
    environment:
      - VPN_SERVICE_PROVIDER=mullvad # or protonvpn, surfshark, custom
      - VPN_TYPE=wireguard
      - WIREGUARD_PRIVATE_KEY=your_key
      - WIREGUARD_ADDRESSES=10.64.0.1/32
    ports:
      - "1080:1080" # Built-in local SOCKS5 proxy

  iptv-proxy:
    # ... existing iptv-proxy configuration ...
```

In the Web Admin, select **Gluetun Sidecar (gluetun:1080)** with no username or password required!

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
http://<YOUR_VPS_IP>:8080/admin/
```

* **Username**: `admin` (or customized in Web Admin -> Users tab)
* **Password**: `admin` (or customized in Web Admin -> Users tab)

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

### 4. Cannot reach Web Admin or Streams (Connection Timed Out / Refused)
1. Verify the container is running and healthy:
   ```bash
   docker ps
   ```
2. Test local connectivity directly on the VPS:
   ```bash
   curl -I http://127.0.0.1:8080/
   ```
3. If it answers locally but you cannot connect from your browser or IPTV player, a firewall is blocking incoming traffic on port 8080:
   * **UFW (Ubuntu/Debian):** Run `ufw allow 8080/tcp && ufw reload`
   * **Oracle Cloud Ubuntu:** Oracle images include strict default iptables rules. Run:
     ```bash
     iptables -I INPUT 6 -m state --state NEW -p tcp --dport 8080 -j ACCEPT
     netfilter-persistent save
     ```
   * **Cloud Provider Web Console:** Check your VPS provider's portal (AWS Security Groups, Oracle Ingress Rules, Google Cloud Firewall, Hetzner Firewall) to ensure port `8080` (TCP) is allowed for inbound traffic from `0.0.0.0/0`.

### 5. 1-Click Web Admin & Safe Git Updates
* **🚀 1-Click Update from Web Admin:** Simply click the **⬆️ Updates** button in the dashboard header and press **`🚀 Update Now`**. The proxy automatically pulls the latest master code from GitHub, rebuilds the Docker container in the background, and refreshes the browser when complete (~35s).
* **💻 Manual Update via Terminal (Optional):**
  ```bash
  cd ~/iptv-proxy
  git pull origin master
  docker compose up -d --build
  ```
* **🛡️ Your settings are 100% safe:** All IPTV providers, users, passwords, NordVPN proxy settings, and filters are stored in `./data/` (`provider.json`, `users.json`, `filters.json`). Git and Docker rebuilds **never** touch or overwrite the `./data/` folder!
* **If git pull says `untracked file docker-compose.yml would be overwritten` (for older installs):**
  ```bash
  cp docker-compose.yml docker-compose.yml.bak
  rm docker-compose.yml
  git pull origin master
  cp docker-compose.yml.bak docker-compose.yml
  git update-index --skip-worktree docker-compose.yml
  docker compose up -d --build
  ```
* **Customizing Ports without modifying tracked files:**
  You can create an optional `.env` file (e.g. `PORT=9000`) or a `docker-compose.override.yml`. Both are ignored by Git, ensuring updates run smoothly with zero conflicts.

---

## 📄 License

GPL-3.0 License. See `LICENSE` for details.
