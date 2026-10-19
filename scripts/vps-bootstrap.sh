#!/usr/bin/env bash
# Bootstrap VPS Debian untuk pengembangan Damn-Bot dari jarak jauh (docs/VPS.md, ADR-105).
#
# Idempoten: setiap langkah aman dijalankan berulang. Tidak ada `curl | bash`, tidak ada
# paket tanpa versi+sha256, dan tidak ada rahasia di baris perintah. Skrip ini TIDAK
# menjalankan `gh auth login` dan tidak pernah membuka RDP/VNC ke internet.
#
# Pemakaian: sudo bash scripts/vps-bootstrap.sh [--code-server] [--desktop] [--apply-ssh]
set -euo pipefail
umask 077

readonly GO_VERSION="1.26.9"
readonly GH_VERSION="2.102.0"
readonly CODE_SERVER_VERSION="4.141.0"
readonly DEV_USER="dev"
readonly SSH_SNIPPET="/etc/ssh/sshd_config.d/99-damnbot.conf"
readonly GO_PROFILE="/etc/profile.d/go.sh"
readonly BASE_PACKAGES=(
	git make curl sudo jq ripgrep build-essential tmux mosh ufw fail2ban
	unattended-upgrades ca-certificates
)

DESKTOP=0
CODE_SERVER=0
APPLY_SSH=0

log() { printf '[vps] %s\n' "$*"; }
warn() { printf '[vps] PERINGATAN: %s\n' "$*" >&2; }
die() {
	printf '[vps] GAGAL: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Pemakaian: sudo bash scripts/vps-bootstrap.sh [opsi]

Menyiapkan VPS Debian untuk pengembangan Damn-Bot: paket dasar, user `dev`, hardening
SSH, ufw, Go, gh CLI, dan batas resource systemd. Aman dijalankan berulang.

Opsi:
  --code-server  Pasang code-server (bind 127.0.0.1:8080, password acak 0600).
  --desktop      Pasang XFCE + xrdp untuk ~dev/.xsession. Port 3389 tetap ditutup
                 ufw; akses hanya lewat SSH tunnel (atau tailnet yang dikendalikan).
  --apply-ssh    Reload sshd setelah menulis snippet hardening. Tanpa opsi ini
                 snippet hanya ditulis (aman dilakukan dari sesi SSH yang ada).
  -h, --help     Tampilkan bantuan ini.

Contoh:
  sudo bash scripts/vps-bootstrap.sh --code-server
  sudo bash scripts/vps-bootstrap.sh --code-server --desktop

Prasyarat: Debian 12/13, akses root, koneksi keluar HTTPS, dan kunci SSH Anda sudah
ada di /root/.ssh/authorized_keys (disalin ke user dev). Dokumentasi: docs/VPS.md.
EOF
}

while (($# > 0)); do
	case "$1" in
	--code-server) CODE_SERVER=1 ;;
	--desktop) DESKTOP=1 ;;
	--apply-ssh) APPLY_SSH=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf '[vps] GAGAL: argumen tidak dikenal: %s\n' "$1" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done

((EUID == 0)) || die "jalankan sebagai root: sudo bash $0"
[[ -r /etc/os-release ]] || die "/etc/os-release tidak ada; bukan Debian?"
# shellcheck disable=SC1091 # file sistem, bukan bagian repo
. /etc/os-release
case " ${ID:-} ${ID_LIKE:-} " in
*debian*) ;;
*) die "distro '${PRETTY_NAME:-$(uname -s)}' bukan Debian; skrip ini khusus Debian 12/13" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "arsitektur $(uname -m) tidak didukung (amd64/arm64)" ;;
esac

tmp=$(mktemp -d /tmp/damnbot-vps.XXXXXX)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

# fetch_verified <url> <sha256> <tujuan>: unduh HTTPS, verifikasi sha256, gagal keras bila beda.
fetch_verified() {
	local url=$1 want=$2 dest=$3
	log "unduh $(basename "$dest")"
	curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --retry-delay 2 -o "$dest" "$url" ||
		die "unduh gagal: $url"
	printf '%s  %s\n' "$want" "$dest" | sha256sum -c --status - ||
		die "sha256 tidak cocok untuk $(basename "$dest"); unduhan dibatalkan"
}

step_base() {
	log "pasang paket dasar: ${BASE_PACKAGES[*]}"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y --no-install-recommends "${BASE_PACKAGES[@]}"
}

step_user() {
	if id -u "$DEV_USER" >/dev/null 2>&1; then
		log "user '$DEV_USER' sudah ada"
	else
		log "buat user '$DEV_USER' (grup sudo, shell bash)"
		useradd --create-home --user-group --shell /bin/bash --groups sudo "$DEV_USER"
	fi
	usermod -aG sudo "$DEV_USER"
	install -d -m 0700 -o "$DEV_USER" -g "$DEV_USER" "/home/$DEV_USER/.ssh"

	if [[ ! -f /root/.ssh/authorized_keys ]]; then
		warn "/root/.ssh/authorized_keys tidak ada; login SSH user dev harus disiapkan manual"
		return 0
	fi
	if [[ -s /home/$DEV_USER/.ssh/authorized_keys ]]; then
		log "authorized_keys milik '$DEV_USER' sudah ada; tidak ditimpa"
	else
		log "salin /root/.ssh/authorized_keys ke user '$DEV_USER'"
		install -m 0600 -o "$DEV_USER" -g "$DEV_USER" \
			/root/.ssh/authorized_keys "/home/$DEV_USER/.ssh/authorized_keys"
	fi
}

step_ssh() {
	install -d -m 0755 /etc/ssh/sshd_config.d
	cat >"$SSH_SNIPPET" <<'EOF'
# Dikelola scripts/vps-bootstrap.sh (docs/VPS.md, ADR-105).
# Hanya login kunci publik; root tidak boleh login lewat SSH.
PasswordAuthentication no
PermitRootLogin no
EOF
	chmod 0644 "$SSH_SNIPPET"

	if ! grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config; then
		warn "/etc/ssh/sshd_config tidak memuat Include sshd_config.d/*.conf; snippet bisa diabaikan"
	fi
	if ! sshd -t 2>/dev/null; then
		warn "sshd -t gagal; periksa /etc/ssh/sshd_config sebelum reload"
	fi

	# Nilai efektif = yang benar-benar dipakai sshd (bukan sekadar isi snippet).
	local effective
	effective=$(sshd -T 2>/dev/null || true)
	if [[ -n $effective ]]; then
		grep -qiE '^passwordauthentication no$' <<<"$effective" ||
			warn "PasswordAuthentication efektif BUKAN 'no'; ada directive lain yang menang"
		grep -qiE '^permitrootlogin no$' <<<"$effective" ||
			warn "PermitRootLogin efektif BUKAN 'no'; ada directive lain yang menang"
	fi

	if ((APPLY_SSH)); then
		[[ -s /home/$DEV_USER/.ssh/authorized_keys ]] ||
			die "tidak ada kunci publik untuk '$DEV_USER'; reload sshd akan mengunci akses"
		sshd -t || die "sshd -t gagal; snippet tidak di-reload"
		systemctl reload ssh 2>/dev/null || systemctl reload sshd ||
			die "reload sshd gagal"
		log "sshd di-reload; sesi baru memakai kebijakan kunci-saja"
	else
		log "snippet SSH ditulis; sshd TIDAK di-reload (pakai --apply-ssh setelah login kunci teruji)"
	fi
}

step_firewall() {
	local port
	port=$(sshd -T 2>/dev/null | awk '$1 == "port" { print $2; exit }' || true)
	[[ $port =~ ^[0-9]+$ ]] || port=22
	log "port SSH efektif: $port"

	ufw default deny incoming
	ufw default allow outgoing
	ufw allow "$port/tcp" comment 'SSH (vps-bootstrap)'
	# Eksplisit walaupun default-deny: RDP/VNC tidak pernah terbuka ke publik.
	ufw deny 3389/tcp comment 'RDP tidak pernah publik'
	ufw deny 5900/tcp comment 'VNC tidak pernah publik'
	ufw --force enable
	log "ufw aktif: masuk ditolak kecuali port $port/tcp"
}

step_go() {
	local current="" sha url archive
	if command -v go >/dev/null 2>&1; then
		current=$(go version || true)
	fi
	if [[ $current == *"go version go$GO_VERSION "* ]]; then
		log "Go $GO_VERSION sudah terpasang; lewati unduhan"
	else
		case "$ARCH" in
		amd64) sha="42d158b4d8f7b61ac0a830567c940a86098fb7aac52e467a5ebec03ef5cc2f8d" ;;
		arm64) sha="4a97373d49fcacdcf3694fea368a500b00ee3e963974f3e7514132717632f052" ;;
		esac
		url="https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz"
		archive="$tmp/go.tar.gz"
		fetch_verified "$url" "$sha" "$archive"
		log "pasang Go $GO_VERSION ke /usr/local/go"
		tar -C "$tmp" -xzf "$archive"
		[[ -x "$tmp/go/bin/go" ]] || die "arsip Go tidak berisi bin/go"
		"$tmp/go/bin/go" version | grep -Fq "go$GO_VERSION " ||
			die "arsip Go tidak melaporkan versi $GO_VERSION"
		rm -rf /usr/local/go
		mv "$tmp/go" /usr/local/go
	fi

	cat >"$GO_PROFILE" <<'EOF'
# Dikelola scripts/vps-bootstrap.sh (docs/VPS.md, ADR-105).
export PATH="/usr/local/go/bin:$PATH"
EOF
	chmod 0644 "$GO_PROFILE"
	export PATH="/usr/local/go/bin:$PATH"
}

step_gh() {
	local sha url archive bin
	if command -v gh >/dev/null 2>&1 &&
		gh --version 2>/dev/null | head -n1 | grep -Fq "version $GH_VERSION"; then
		log "gh CLI $GH_VERSION sudah terpasang; lewati unduhan"
		return 0
	fi
	case "$ARCH" in
	amd64) sha="bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386" ;;
	arm64) sha="7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484" ;;
	esac
	url="https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${ARCH}.tar.gz"
	archive="$tmp/gh.tar.gz"
	fetch_verified "$url" "$sha" "$archive"
	mkdir -p "$tmp/gh"
	tar -C "$tmp/gh" -xzf "$archive"
	bin=$(find "$tmp/gh" -type f -path '*/bin/gh' -print -quit)
	[[ -n $bin ]] || die "arsip gh tidak berisi bin/gh"
	install -m 0755 "$bin" /usr/local/bin/gh
	hash -r
	gh --version | head -n1 | grep -Fq "version $GH_VERSION" ||
		die "gh terpasang tetapi versinya bukan $GH_VERSION"
	log "gh CLI $GH_VERSION terpasang di /usr/local/bin/gh (belum login)"
}

step_user_slice() {
	local uid dropin
	uid=$(id -u "$DEV_USER")
	dropin="/etc/systemd/system/user-${uid}.slice.d"
	install -d -m 0755 "$dropin"
	cat >"$dropin/50-damnbot-limits.conf" <<'EOF'
# Dikelola scripts/vps-bootstrap.sh (docs/VPS.md, ADR-105).
# Build/test tidak boleh menghabiskan seluruh VPS. Batas berlaku untuk semua proses
# user dev dan aktif pada login/sesi berikutnya (slice dibuat systemd saat login).
[Slice]
CPUQuota=350%
MemoryMax=6G
EOF
	chmod 0644 "$dropin/50-damnbot-limits.conf"
	systemctl daemon-reload
	log "batas user-$uid.slice: CPU 350%, RAM 6G (aktif pada sesi berikutnya)"
}

step_code_server() {
	local sha url archive dir cfg
	case "$ARCH" in
	amd64) sha="65bcd20b524d03eb1b5adb7f360244599ef0e5b74089a46a982d32c074b97a29" ;;
	arm64) sha="e5cc78da1d631c6cf6162f9b18eeaa8ee9d3239e1a88bb99d0602cdc5fa1852d" ;;
	esac

	if [[ -x /usr/lib/code-server/bin/code-server ]] &&
		/usr/lib/code-server/bin/code-server --version 2>/dev/null | grep -Fq "$CODE_SERVER_VERSION"; then
		log "code-server $CODE_SERVER_VERSION sudah terpasang; lewati unduhan"
	else
		url="https://github.com/coder/code-server/releases/download/v${CODE_SERVER_VERSION}/code-server-${CODE_SERVER_VERSION}-linux-${ARCH}.tar.gz"
		archive="$tmp/code-server.tar.gz"
		fetch_verified "$url" "$sha" "$archive"
		log "pasang code-server $CODE_SERVER_VERSION ke /usr/lib/code-server"
		tar -C "$tmp" -xzf "$archive"
		dir=$(find "$tmp" -maxdepth 1 -type d -name "code-server-${CODE_SERVER_VERSION}-linux-${ARCH}" -print -quit)
		[[ -n $dir ]] || die "arsip code-server tidak berisi direktori yang diharapkan"
		rm -rf /usr/lib/code-server
		mv "$dir" /usr/lib/code-server
		ln -sfn /usr/lib/code-server/bin/code-server /usr/local/bin/code-server
		hash -r
	fi

	install -d -m 0700 -o "$DEV_USER" -g "$DEV_USER" "/home/$DEV_USER/.config/code-server"
	cfg="/home/$DEV_USER/.config/code-server/config.yaml"
	if [[ -s $cfg ]]; then
		log "config code-server sudah ada; password tidak diubah"
		chown "$DEV_USER:$DEV_USER" "$cfg"
		chmod 0600 "$cfg"
		if ! grep -qE '^bind-addr:[[:space:]]*127\.0\.0\.1:8080' "$cfg"; then
			warn "bind-addr di $cfg bukan 127.0.0.1:8080; jangan buka code-server ke publik"
		fi
	else
		# Password acak dari /dev/urandom; tidak pernah lewat argv atau layar.
		local pw
		pw=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
		cat >"$cfg" <<EOF
bind-addr: 127.0.0.1:8080
auth: password
password: $pw
cert: false
EOF
		chown "$DEV_USER:$DEV_USER" "$cfg"
		chmod 0600 "$cfg"
		log "config code-server ditulis ke $cfg (password acak, mode 0600)"
	fi
}

step_desktop() {
	log "pasang XFCE + xrdp (port 3389 tetap ditolak ufw)"
	export DEBIAN_FRONTEND=noninteractive
	apt-get install -y xfce4 xrdp
	printf '%s\n' 'startxfce4' >"/home/$DEV_USER/.xsession"
	chown "$DEV_USER:$DEV_USER" "/home/$DEV_USER/.xsession"
	chmod 0755 "/home/$DEV_USER/.xsession"
	systemctl enable --now xrdp
	log "xrdp aktif di port 3389; ufw menolak akses publik (lihat ringkasan)"
}

summary() {
	cat <<'EOF'

[vps] Selesai. Ringkasan langkah berikutnya:

  1. Login sebagai user dev:              sudo -iu dev
  2. Uji login kunci dari ponsel/laptop:  ssh dev@<ip-vps>
     (ulangi skrip dengan --apply-ssh HANYA setelah login kunci terbukti.)
  3. Pasang Tailscale (repo resmi, ikuti docs): https://tailscale.com/download/linux
     lalu `sudo tailscale up`; akses VPS lewat nama tailnet, bukan IP publik.
  4. Autentikasi GitHub (interaktif, token tidak pernah di argv):
     gh auth login
  5. Clone dan uji:   git clone https://github.com/yuldan/damn-bot.git
                      cd damn-bot && make test
  6. Go 1.26.9 dan batas resource user slice aktif pada sesi/login baru
     (atau `source /etc/profile.d/go.sh` untuk PATH Go).
EOF
	if ((CODE_SERVER)); then
		cat <<'EOF'

  code-server (loopback saja):
     ssh -L 8080:127.0.0.1:8080 dev@<vps>   lalu buka http://127.0.0.1:8080
     Password ada di ~dev/.config/code-server/config.yaml (mode 0600).
EOF
	fi
	if ((DESKTOP)); then
		cat <<'EOF'

  Desktop XFCE via xrdp (TIDAK terbuka ke internet; ufw menolak 3389):
     ssh -L 3389:127.0.0.1:3389 dev@<vps>
     lalu sambungkan klien RDP ke 127.0.0.1:3389.
     Alternatif Tailscale: buka 3389 hanya di antarmuka tailscale0 untuk anggota
     tailnet yang Anda percaya; jangan pernah membukanya di antarmuka publik.
EOF
	fi
}

log "mulai bootstrap Damn-Bot VPS (Debian, arch $ARCH)"
step_base
step_user
step_ssh
step_firewall
step_go
step_gh
step_user_slice
if ((CODE_SERVER)); then step_code_server; fi
if ((DESKTOP)); then step_desktop; fi
summary
