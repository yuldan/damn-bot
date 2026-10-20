# VPS Debian untuk pengembangan Damn-Bot dari ponsel

Status: panduan operasional (bukan requirement produk; ADR-107). Skripnya:
[`scripts/vps-bootstrap.sh`](../scripts/vps-bootstrap.sh). Jalankan `--help` sebelum memakai.

Tujuannya satu: repo ini bisa dibangun, diuji, dan dikerjakan dari ponsel terhadap satu VPS
Debian, tanpa desktop jarak jauh yang terbuka ke internet. Sumber kebenaran tetap GitHub
(branch + PR); VPS boleh dihapus dan dibuat ulang kapan saja.

---

## 1. Spesifikasi

| Komponen | Minimum | Rekomendasi | Alasan |
|---|---|---|---|
| vCPU | 2 | 4 | `go test -race` + `go build` paralel; code-server ikut memakai CPU |
| RAM | 4 GB | 8 GB | race detector memperbesar memori test; 4 GB bisa OOM saat `make test` penuh |
| Disk | 40 GB | 80–160 GB NVMe | modul Go, cache build, korpus, snapshot; NVMe mempercepat `go test` |
| Swap | 2 GB | 4 GB | jaring pengaman saat build; bukan pengganti RAM |
| Bandwidth | 1 TB/bulan | 2 TB/bulan | modul Go, code-server, korpus; tidak ada media besar |
| Region | terdekat | Singapura/Jakarta (atau terdekat ponsel) | latensi SSH/mosh dan code-server |
| OS | Debian 12 (bookworm) | Debian 12; Debian 13 juga didukung | kernel 6.1 ≥ 5.6 untuk `openat2`, systemd, ufw |

Tidak perlu GPU. RDP bukan kebutuhan; kalau memakai desktop, itu opsi kedua (§5).

## 2. Mengapa Debian 12, bukan Windows + RDP

- **Perilaku yang diuji memang Unix.** `make test` menjalankan test dengan build tag
  `linux || darwin` (mis. proses grup di `internal/ctxdb/scenario_proc_test.go`), dan
  `internal/sandbox` memakai `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)` di Linux ≥ 5.6
  untuk anti-TOCTOU. Semantik setgid, proses grup/sinyal, device+inode, dan permission bit
  Unix tidak bisa dibuktikan di Windows. Di Linux VPS, jalur yang dipakai produksi adalah
  jalur yang diuji.
- **CI sudah menutup portabilitas.** Matriks `.github/workflows/ci.yml` menjalankan suite di
  linux amd64/arm64, darwin amd64/arm64, dan windows amd64/arm64 (plus job `windows-ctxdb`).
  Jadi Windows/macOS tidak perlu menjadi mesin pengembangan.
- **Biaya dan permukaan serang.** Windows Server butuh lisensi + RAM lebih besar, dan RDP
  yang terbuka ke internet adalah target brute force. Debian + SSH kunci + ufw + Tailscale
  lebih kecil permukaannya.
- **Alat yang dipakai repo tersedia langsung:** `make`, `tmux`, `mosh`, `ufw`,
  `fail2ban`, `unattended-upgrades`, dan systemd slice untuk membatasi build.

Ubuntu LTS juga secara teknis bisa, tetapi skrip ini sengaja menolak distro non-Debian agar
hasilnya dapat diprediksi. Debian 13 (trixie) memakai kernel/systemd yang lebih baru dan
tetap cocok.

**Rekomendasi tegas untuk proyek ini: Debian 12 + XFCE (atau Cinnamon).** Linux Mint
adalah distro desktop: tidak ada edisi server resmi, image VPS-nya tidak umum, dan
paketnya lebih berat. Tampilan Mint tetap bisa didekati di Debian dengan Cinnamon
(`scripts/vps-desktop.sh --cinnamon`). Windows RDP tetap tidak disarankan karena test
Unix-only (proses grup, `openat2`, sandbox) tidak bisa berjalan di sana.

## 3. Setup

Prasyarat: VPS Debian 12/13, akses root, dan kunci SSH Anda sudah ada di
`/root/.ssh/authorized_keys` (skrip menyalinnya ke user `dev`). Skrip **tidak** mengatur
DNS, tidak membuat kunci, dan tidak menjalankan `gh auth login`.

```bash
# di laptop/ponsel, kirim skrip ke VPS
scp scripts/vps-bootstrap.sh root@<ip-vps>:/root/

# di VPS
sudo bash /root/vps-bootstrap.sh --help
sudo bash /root/vps-bootstrap.sh --code-server
```

Varian:

| Perintah | Yang ditambahkan |
|---|---|
| `sudo bash scripts/vps-bootstrap.sh` | paket dasar, user `dev`, snippet SSH, ufw, Go, gh, slice resource |
| `... --code-server` | code-server 4.141.0, bind `127.0.0.1:8080`, password acak di `~dev/.config/code-server/config.yaml` (0600) |
| `... --desktop` | XFCE + xrdp, `~dev/.xsession` = `startxfce4`, port 3389 tetap ditolak ufw |
| `... --apply-ssh` | reload sshd setelah snippet hardening ditulis dan lolos `sshd -t` |

Tanpa `--apply-ssh`, snippet ditulis ke `/etc/ssh/sshd_config.d/99-damnbot.conf` tetapi
sshd tidak direload. Ini disengaja: jalankan `--apply-ssh` **hanya setelah** login kunci ke
user `dev` terbukti berhasil, supaya tidak terkunci.

Semua langkah idempoten. Menjalankan ulang skrip tidak mengganti password code-server yang
sudah ada, tidak menimpa `authorized_keys` user `dev`, dan melewati unduhan yang versinya
sudah cocok (Go 1.26.9, gh 2.102.0, code-server 4.141.0). Unduhan memakai HTTPS + sha256
yang dipin di skrip; tidak ada `curl | bash`.

Verifikasi setelah bootstrap:

```bash
bash -n scripts/vps-bootstrap.sh
shellcheck scripts/vps-bootstrap.sh   # bila tersedia
scripts/vps-bootstrap.sh --help
go version                            # go1.26.9 linux/amd64 (atau arm64)
gh --version                          # gh 2.102.0, belum login
sudo ufw status verbose               # deny incoming, allow <port-ssh>/tcp, deny 3389/5900
cat "/etc/systemd/system/user-$(id -u dev).slice.d/50-damnbot-limits.conf"
```

Batas resource user `dev` adalah `CPUQuota=350%` dan `MemoryMax=6G` (drop-in
`/etc/systemd/system/user-<uid>.slice.d/50-damnbot-limits.conf`). Slice baru terbentuk saat
user login, jadi batas aktif pada sesi berikutnya.

## 4. Checklist hardening

- [ ] `PasswordAuthentication no` dan `PermitRootLogin no` efektif —
      `sudo sshd -T | grep -E '^(passwordauthentication|permitrootlogin)'`.
- [ ] ufw aktif dan hanya port SSH yang terbuka; `ufw status verbose` tidak memuat
      `ALLOW` untuk 3389/5900.
- [ ] fail2ban aktif dan jail sshd terdeteksi:
      `sudo systemctl enable --now fail2ban && sudo fail2ban-client status sshd`.
- [ ] `unattended-upgrades` aktif untuk update keamanan:
      `cat /etc/apt/apt.conf.d/20auto-upgrades`.
- [ ] code-server hanya di loopback: `ss -ltnp | grep 8080` menunjuk `127.0.0.1:8080`.
- [ ] xrdp (bila `--desktop`) tidak terbuka ke publik. Untuk pertahanan berlapis, ubah
      `port=tcp://127.0.0.1:3389` di `/etc/xrdp/xrdp.ini` lalu `systemctl restart xrdp`.
- [ ] Tailscale hanya untuk anggota tailnet; aktifkan ACL/tag dan jangan pernah memasang
      `ufw allow 3389` di antarmuka publik.
- [ ] `~/.config/gh/hosts.yml` dan `~/.config/code-server/config.yaml` bermode 0600 dan tidak
      pernah masuk repo.
- [ ] `gh auth login` dijalankan interaktif di VPS; token tidak pernah lewat argumen atau
      `git remote` berisi kredensial.
- [ ] Reboot berkala setelah upgrade kernel; cek `sudo needrestart -r l` bila tersedia.
- [ ] Disk tidak penuh (cache build + korpus): `df -h /`, `du -sh ~/.cache/damnbot`.

## 5. Akses dari ponsel

Urutannya: **Tailscale → SSH kunci → mosh → tmux → code-server (opsional)**.

1. **Tailscale** di VPS dan ponsel. Ikuti instruksi resmi
   <https://tailscale.com/download/linux> (repo apt resmi; jangan memakai skrip pipa tanpa
   pinning). Setelah `sudo tailscale up`, akses VPS lewat nama tailnet/MagicDNS, bukan IP
   publik. Tailscale SSH opsional; SSH biasa + kunci tetap paling sederhana.
2. **Klien SSH di ponsel:** Termius (iOS/Android) atau Blink Shell (iOS). Impor kunci
   privat ke klien, bukan ke VPS; gunakan passphrase.
3. **tmux** supaya sesi bertahan saat sinyal ponsel hilang:
   ```bash
   tmux new -As damnbot     # buat atau sambung sesi "damnbot"
   # Ctrl-b d untuk detach; `tmux ls` untuk daftar sesi
   ```
4. **mosh** untuk koneksi yang tahan roam/ganti jaringan:
   ```bash
   mosh dev@<vps-tailnet> -- tmux new -As damnbot
   ```
   mosh memakai UDP 60000–61000. Dengan ufw default-deny, buka hanya untuk tailnet:
   ```bash
   sudo ufw allow in on tailscale0 to any port 60000:61000 proto udp
   ```
   Jangan membuka rentang itu di antarmuka publik.
5. **code-server** untuk mengedit dari browser ponsel; lihat §8.

Alur khas: buka Termius → `mosh dev@vps -- tmux new -As damnbot` → `gh pr list`,
`git switch`, `make test`, lalu detach. Pekerjaan tetap hidup di tmux walaupun aplikasi
ponsel ditutup.

## 6. Jalur tanpa SSH (RDP-first)

Kalau SSH tidak nyaman, VPS bisa dibuat langsung dengan desktop + xrdp + Tailscale lewat
cloud-init. User `dev`, password, dan auth key Tailscale diisi sebelum VPS dibuat; setelah
boot, sambungkan aplikasi RDP ke nama MagicDNS tailnet. Tidak ada port 3389 yang terbuka
ke internet.

Berkas siap pakai: [`deploy/cloud-init.yaml`](../deploy/cloud-init.yaml). Salin isinya ke
kolom "user data"/"cloud config" penyedia VPS (Hetzner, DigitalOcean, Vultr, dsb.) setelah
mengganti dua placeholder:

1. `passwd` — hash SHA-512: `openssl passwd -6 'password-anda'`.
2. `CHANGE_ME_TS_AUTHKEY` — auth key Tailscale sekali pakai dari
   <https://login.tailscale.com/admin/settings/keys>.

Jangan commit salinan yang sudah berisi hash/auth key. Isi `deploy/cloud-init.yaml` harus
sama dengan blok berikut:

```yaml
#cloud-config
# Bootstrap "RDP-first" untuk VPS Debian: desktop + xrdp + Tailscale tanpa SSH.
# Dokumen lengkap: docs/VPS.md §6.
#
# WAJIB DIGANTI sebelum dipakai (jangan commit salinan yang sudah diisi):
#  1. passwd: hash SHA-512 password user dev.
#       openssl passwd -6 'password-anda'
#     Alternatif plaintext (tidak disarankan untuk file yang disimpan):
#       chpasswd: [{name: dev, password: "password-anda", type: text}]
#  2. TS_AUTHKEY: auth key Tailscale sekali pakai
#       https://login.tailscale.com/admin/settings/keys
#
# UFW: 3389 ditolak untuk publik; hanya antarmuka tailscale0 yang diizinkan.

package_update: true

packages:
  - xfce4
  - xfce4-goodies
  - xrdp
  - xorgxrdp
  - dbus-x11
  - ufw
  - curl
  - ca-certificates
  - sudo

users:
  - name: dev
    gecos: Damn-Bot developer
    groups: [sudo]
    shell: /bin/bash
    lock_passwd: false
    # Hash SHA-512 dari password pilihan Anda (lihat header di atas).
    passwd: "CHANGE_ME"

write_files:
  - path: /home/dev/.xsession
    owner: dev
    permissions: '0755'
    content: |
      startxfce4
  - path: /home/dev/.config/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml
    owner: dev
    permissions: '0600'
    content: |
      <?xml version="1.0" encoding="UTF-8"?>
      <channel name="xfwm4" version="1.0">
        <property name="general" type="empty">
          <property name="use_compositing" type="bool" value="false"/>
        </property>
      </channel>

runcmd: |
  # Tidak memakai `set -e`: satu langkah gagal tidak boleh membatalkan langkah lain.
  set -u
  systemctl enable --now xrdp
  ufw default deny incoming
  ufw default allow outgoing
  ufw deny 3389/tcp
  ufw --force enable
  codename=$(. /etc/os-release && echo "$VERSION_CODENAME")
  curl -fsSL "https://pkgs.tailscale.com/stable/debian/${codename}.noarmor.gpg" \
    -o /usr/share/keyrings/tailscale-archive-keyring.gpg
  curl -fsSL "https://pkgs.tailscale.com/stable/debian/${codename}.tailscale-keyring.list" \
    -o /etc/apt/sources.list.d/tailscale.list
  apt-get update
  apt-get install -y tailscale
  # GANTI TS_AUTHKEY. Tanpa auth key, jalankan `sudo tailscale up` lewat konsol penyedia.
  tailscale up --auth-key=CHANGE_ME_TS_AUTHKEY --hostname=damnbot-vps || true
  # tailnet boleh RDP; rule disisipkan di atas DENY 3389 agar menang urutan.
  ufw insert 1 allow in on tailscale0 to any port 3389 proto tcp || true

final_message: "Damn-Bot RDP-first siap. Sambungkan Windows App ke nama MagicDNS VPS (port 3389) lewat Tailscale."
```

Setelah VPS boot (±3–5 menit):

1. Pasang Tailscale dan **Windows App** (Microsoft Remote Desktop) di HP; login akun tailnet
   yang sama dengan auth key.
2. Di Windows App: **Add PC** → host `damnbot-vps` (MagicDNS) atau IP tailnet, user `dev`,
   password yang Anda set, port 3389.
3. Ganti password setelah login pertama (`passwd` di terminal desktop).
4. Verifikasi 3389 tidak publik: `sudo ufw status numbered`. Harus ada `ALLOW … tailscale0`
   di atas `DENY 3389`; jangan pernah menjalankan `ufw allow 3389`.
5. Jika login gagal, buka konsol penyedia dan periksa
   `/var/log/cloud-init-output.log`. Penyebab umum: `passwd` masih `CHANGE_ME` (bukan hash)
   atau auth key Tailscale sudah kedaluwarsa.
6. Jalur RDP tidak bergantung pada SSH. Nanti, bila ingin menambah SSH,
   jalankan `scripts/vps-bootstrap.sh --apply-ssh` setelah `authorized_keys` disiapkan.

Cloud-init di atas memakai XFCE. Untuk Cinnamon (tampilan Mint), jalankan
`scripts/vps-desktop.sh --cinnamon` setelah masuk lewat RDP.

## 7. Akses grafis dari HP

Semua opsi di bawah adalah **alat host**, bukan dependency proyek: `deps.allow`, budget LOC,
dan aturan ADR tidak berlaku untuk paket/aplikasi yang dipasang di VPS. AnyDesk sendiri
proprietary; alternatif open source di bawah mencakup kebutuhan "lihat desktop dari HP".

| Opsi | Lisensi | Cara akses | Kapan dipakai | Catatan |
|---|---|---|---|---|
| xrdp + Windows App + Tailscale | Apache-2.0 | klien RDP | baseline; paling sedikit moving parts | Sesi X per koneksi; tidak butuh desktop selalu hidup |
| Apache Guacamole | Apache-2.0 | browser, clientless | paling nyaman di HP | Docker (guacd + webapp); mem-proxy RDP/VNC, jadi tetap memakai xrdp |
| RustDesk | AGPL-3.0 | aplikasi RustDesk | paling mirip AnyDesk | Menangkap **sesi X yang sedang berjalan**; headless wajib punya sesi persisten |
| MeshCentral | Apache-2.0 | web console | alternatif agent-based | Server Node.js; fitur lebih luas, lebih berat |
| noVNC + TigerVNC | MPL-2.0 / GPLv2 | browser VNC | paling sederhana untuk browser | VNC tidak terenkripsi; hanya tailnet |
| Sunshine + Moonlight | GPL-3.0 | aplikasi Moonlight | streaming latensi rendah | Butuh encoder/GPU; lebih untuk game/desktop penuh |

### 7.1 xrdp + Windows App + Tailscale (baseline)

Lihat §6 untuk cloud-init dan §8 untuk `scripts/vps-desktop.sh`. Ini opsi dengan langkah
paling sedikit: satu port (3389) yang hanya diizinkan di `tailscale0`, satu sesi X per
koneksi, dan tidak perlu menjaga sesi desktop tetap hidup.

### 7.2 Apache Guacamole (clientless, cocok untuk HP)

Guacamole menjalankan `guacd` + webapp di Docker, lalu mem-proxy RDP ke xrdp. Browser HP
hanya membuka satu URL; tidak ada aplikasi RDP tambahan.

```bash
# di VPS: xrdp harus sudah ada
sudo bash scripts/vps-desktop.sh

# Docker Engine + Compose (Debian)
sudo apt-get install -y docker.io docker-compose
cd deploy/guacamole
cp user-mapping.xml.example user-mapping.xml
chmod 600 user-mapping.xml      # ganti CHANGE_ME dan CHANGE_ME_RDP
docker compose up -d            # atau: docker-compose up -d

# HTTPS tanpa membuka port web ke publik: Tailscale Serve meneruskan ke loopback
sudo tailscale serve --bg 8080
```

Buka `https://<nama-magicdns>/` dari HP, login user `dev` (password di
`user-mapping.xml`), lalu pilih koneksi RDP. Compose mengikat webapp ke
`127.0.0.1:8080`; jangan ubah menjadi `0.0.0.0`. `guacd` memakai network host agar bisa
menghubungi xrdp di `127.0.0.1:3389`; tutup port bantunya:

```bash
sudo ufw deny 4822/tcp
```

`user-mapping.xml` berisi password RDP plaintext sesuai format Guacamole. Lindungi dengan
`chmod 600`, jangan commit, dan ganti dengan database (`guacamole-auth-jdbc-*`) bila butuh
banyak user/2FA.

### 7.3 RustDesk (paling mirip AnyDesk)

RustDesk menangkap **display yang sedang berjalan**, bukan membuat sesi baru seperti xrdp.
Di VPS headless tidak ada display sampai Anda membuatnya; karena itu RustDesk butuh sesi X
persisten (Xvfb + desktop, atau sesi XFCE yang selalu hidup). Ini lebih banyak moving parts
daripada xrdp untuk sebuah server.

```bash
# contoh sesi X persisten (headless)
sudo apt-get install -y xvfb xfce4 dbus-x11
Xvfb :1 -screen 0 1280x720x24 &
DISPLAY=:1 startxfce4 &
```

Pasang paket RustDesk dari rilis resmi (pin versi dan verifikasi sha256), set password
permanen, aktifkan **direct IP access**, lalu hubungkan aplikasi RustDesk di HP ke IP
tailnet VPS. Direct-IP lewat Tailscale menghindari relay publik RustDesk; self-hosting
`hbbs`/`hbbr` opsional dan menambah komponen. Jangan pernah membuka 21115–21119 ke
internet.

### 7.4 Opsi lain

- **MeshCentral** (Apache-2.0): web console + agent; berguna bila kelak ada beberapa mesin,
  tetapi server Node.js-nya lebih berat daripada xrdp.
- **noVNC + TigerVNC** (MPL-2.0/GPLv2): `tigervnc-standalone-server` + `websockify`; paling
  cepat disiapkan untuk browser, tetapi VNC tidak terenkripsi sehingga hanya boleh di
  tailnet.
- **Sunshine + Moonlight** (GPL-3.0): streaming latensi rendah dengan encoder; butuh
  CPU/GPU yang memadai dan aplikasi Moonlight di HP.

### 7.5 Aturan keamanan untuk semua opsi

- Bind ke `127.0.0.1` atau antarmuka Tailscale saja; jangan pernah ke `0.0.0.0` publik.
- Jangan buka 3389 (RDP), 5900 (VNC), atau 21115–21119 (RustDesk) di internet.
- Gunakan password kuat; aktifkan 2FA bila tersedia (Guacamole via ekstensi database,
  MeshCentral, Tailscale ACL).
- Tailscale ACL/tag membatasi siapa yang boleh mencapai port desktop.
- Ini alat host, bukan dependency repo; jangan menambahkan image/paketnya ke `go.mod`,
  `deps.allow`, atau budget LOC.

## 8. Opsi GUI

**code-server (disarankan).** Ringan, berjalan sebagai user `dev`, dan hanya mendengarkan
di loopback:

```bash
sudo -iu dev
code-server                    # membaca ~/.config/code-server/config.yaml
```

Dari ponsel/laptop:

```bash
ssh -L 8080:127.0.0.1:8080 dev@<vps>
# buka http://127.0.0.1:8080, login dengan password di config.yaml
```

Jangan mengubah `bind-addr` menjadi `0.0.0.0` dan jangan membuka port 8080 di ufw.

**XFCE + xrdp (opsional, `--desktop`).** Desktop penuh hanya untuk keperluan sesekali.
Port 3389 ditolak ufw dan tidak boleh dibuka ke publik. Akses lewat tunnel:

```bash
ssh -L 3389:127.0.0.1:3389 dev@<vps>
# sambungkan klien RDP ke 127.0.0.1:3389
```

**Pasang desktop di box yang sudah ada.** `scripts/vps-desktop.sh` memasang XFCE (default)
atau Cinnamon (`--cinnamon`) + xrdp, menulis `~dev/.xsession`, mematikan compositor XFCE
(atau efek Cinnamon), mengatur resolusi awal `--res 1280x720|1920x1080`, dan menutup 3389
untuk publik:

```bash
sudo bash scripts/vps-desktop.sh
sudo bash scripts/vps-desktop.sh --cinnamon --res 1920x1080
```

Catatan: xorgxrdp mengambil ukuran layar dari klien; `--res` menambahkan `xrandr`
best-effort di awal sesi, jadi setel ukuran yang sama di aplikasi RDP. Cinnamon/Muffin
tidak menyediakan sakelar mematikan compositor total; skrip mematikan efek dan
meng-unredirect jendela full-screen.

Alternatif Tailscale: buka 3389 hanya di antarmuka `tailscale0` untuk anggota tailnet yang
Anda percaya. Tetap jangan membukanya di antarmuka publik. VNC (5900) tidak dipakai dan
ditolak ufw.

## 9. Resource untuk build dan test

- `make test` menjalankan `go test -race` untuk seluruh paket (lihat `Makefile`). Race
  detector butuh cgo/gcc (sudah ada lewat `build-essential`) dan memori jauh lebih besar
  daripada test biasa.
- Di VPS 4 GB, batasi paralelisme: `go test -race -p 2 ./...`, atau uji paket tertentu
  lebih dulu, mis. `make test PKGS=./internal/sandbox/...`.
- Slice user `dev` membatasi 350% CPU dan 6 GB RAM. Ini melindungi sshd/code-server dari
  build yang lepas kendali; bila build butuh lebih, naikkan drop-in secara sadar, jangan
  menghapusnya.
- Cache dan korpus yang dapat dibangun ulang:
  - `~/.cache/damnbot` — korpus/unduhan turunan (boleh dihapus; akan dibuat ulang).
  - `~/go/pkg/mod` dan `~/.cache/go-build` — modul dan cache build Go.
  - `du -sh ~/.cache/damnbot ~/go/pkg/mod ~/.cache/go-build` untuk memantau.
  - `go clean -cache` (cache build) dan `go clean -modcache` (unduh ulang modul) bila disk
    sempit.
- Runtime produk memakai `<data_dir>` (`$XDG_DATA_HOME/damnbot`, biasanya
  `~/.local/share/damnbot`) untuk DB/blob; jangan mencampurnya dengan cache.
- Tambahkan swap 2–4 GB dan pantau `free -h`; OOM killer pada build biasanya berarti RAM
  kurang, bukan bug test.

## 10. Backup dan update

- **Sumber kebenaran = GitHub.** Commit dan push branch/PR sebelum meninggalkan sesi;
  jangan mengandalkan disk VPS.
- Yang layak dicadangkan (kecil, berisi kredensial/setting):
  - `~/.config/gh/` (token gh; mode 0600),
  - `~/.config/code-server/config.yaml` (password code-server),
  - `~/.omp/` bila ada (state asisten lokal),
  - opsional `~/.ssh/` (kunci yang dibuat di VPS).
  Buat arsip dengan `umask 077`, simpan terenkripsi, dan jangan pernah commit.
- `~/.cache/damnbot`, cache Go, dan `~/.local/share/damnbot` tidak wajib dicadangkan;
  semuanya dapat dibangun ulang dari repo.
- Update rutin: `sudo apt update && sudo apt full-upgrade`, reboot bila kernel berubah,
  lalu jalankan ulang `scripts/vps-bootstrap.sh` (idempoten) untuk memastikan paket dan
  konfigurasi tetap sesuai.
- Snapshot VPS dari panel penyedia sebelum upgrade besar; VPS harus dianggap sekali pakai.
- Versi yang dipin di skrip (Go, gh, code-server) dinaikkan lewat PR tersendiri: ubah
  versi + sha256 bersama-sama, lalu jalankan `bash -n` dan `shellcheck`.

## 11. Migrasi dari laptop

Yang perlu dipindahkan hanyalah pekerjaan yang belum di-push dan, bila diinginkan, dua
direktori konfigurasi:

1. **Push dulu dari laptop.** Commit semua perubahan ke branch, buka/update PR, lalu
   pastikan `gh pr list --author @me --state open` menampilkan pekerjaan yang sama di VPS.
   Jangan menyalin working tree; state hidup di branch/PR.
2. **Clone di VPS:** `git clone https://github.com/yuldan/damn-bot.git && cd damn-bot`.
3. **Autentikasi ulang lebih aman:** `gh auth login` di VPS. Bila ingin menyalin sesi gh,
   salin `~/.config/gh/hosts.yml` dengan mode 0600 dan sadari file itu berisi token.
4. **`~/.omp`** hanya berisi state asisten; salin bila ingin riwayat/konfigurasi yang sama.
   Tidak ada state repo yang dibutuhkan dari laptop.
5. Jalankan `go version`, `gh --version`, lalu `make test` di VPS. Test Linux-only
   (proses grup, `openat2`, sandbox) kini berjalan di lingkungan yang sama dengan produksi;
   itulah keuntungan utama pindah ke VPS.
6. Buka `tmux new -As damnbot` dan lanjutkan dari ponsel; sesi bertahan saat koneksi putus.

## 12. Rujukan

- [`scripts/vps-bootstrap.sh`](../scripts/vps-bootstrap.sh) — bootstrap idempoten.
- [`scripts/vps-desktop.sh`](../scripts/vps-desktop.sh) — XFCE/Cinnamon + xrdp di box yang sudah ada.
- [`deploy/cloud-init.yaml`](../deploy/cloud-init.yaml) — user-data RDP-first (tanpa SSH).
- [`deploy/guacamole/docker-compose.yml`](../deploy/guacamole/docker-compose.yml) — Guacamole clientless.
- [`docs/ENGINEERING.md`](ENGINEERING.md) — standar kode, CI, dan batas ukuran PR (E-84).
- [`docs/DECISIONS.md`](DECISIONS.md) — ADR-107 (keputusan lingkungan VPS).
- [`SPEC.md`](../SPEC.md) — spesifikasi produk; VPS bukan bagian dari requirement produk.
