#!/usr/bin/env bash
# Pasang desktop grafis + xrdp pada VPS Debian yang SUDAH di-bootstrap
# (docs/VPS.md §8, ADR-107). Idempoten.
#
# XFCE adalah default; --cinnamon memberi tampilan yang paling dekat dengan Linux Mint.
# Skrip ini tidak membuka 3389 ke publik: hanya loopback/tailnet. Skrip tidak memasang
# Tailscale (itu langkah terpisah di docs/VPS.md), tetapi menambahkan pengecualian ufw
# untuk antarmuka tailscale0 bila Tailscale sudah ada.
#
# Pemakaian: sudo bash scripts/vps-desktop.sh [--cinnamon] [--res 1280x720|1920x1080]
set -euo pipefail
umask 077

readonly DEV_USER="dev"
readonly XRDP_PORT="3389"

DESKTOP="xfce"
RES="1280x720"

log() { printf '[desktop] %s\n' "$*"; }
warn() { printf '[desktop] PERINGATAN: %s\n' "$*" >&2; }
die() {
	printf '[desktop] GAGAL: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'EOF'
Pemakaian: sudo bash scripts/vps-desktop.sh [opsi]

Memasang XFCE (default) atau Cinnamon + xrdp pada VPS Debian yang sudah punya user
`dev`. Port 3389 tetap ditutup untuk publik; akses hanya lewat Tailscale (atau SSH
tunnel). Aman dijalankan berulang.

Opsi:
  --cinnamon        Pasang Cinnamon (mirip Linux Mint) alih-alih XFCE.
  --xfce            Pasang XFCE (default).
  --res <WxH>       Resolusi awal 1280x720 (default) atau 1920x1080. xorgxrdp tetap
                    mengikuti ukuran dari klien RDP; nilai ini dipasang lewat xrandr
                    di awal sesi sebagai best-effort.
  -h, --help        Tampilkan bantuan ini.

Contoh:
  sudo bash scripts/vps-desktop.sh
  sudo bash scripts/vps-desktop.sh --cinnamon --res 1920x1080
EOF
}

while (($# > 0)); do
	case "$1" in
	--cinnamon) DESKTOP="cinnamon" ;;
	--xfce) DESKTOP="xfce" ;;
	--res)
		(($# >= 2)) || die "--res membutuhkan nilai (1280x720 atau 1920x1080)"
		RES=$2
		shift
		;;
	--res=*) RES=${1#*=} ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf '[desktop] GAGAL: argumen tidak dikenal: %s\n' "$1" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done

((EUID == 0)) || die "jalankan sebagai root: sudo bash $0"
case "$RES" in
1280x720 | 1920x1080) ;;
*) die "resolusi '$RES' tidak didukung; pakai 1280x720 atau 1920x1080" ;;
esac

[[ -r /etc/os-release ]] || die "/etc/os-release tidak ada; bukan Debian?"
# shellcheck disable=SC1091 # file sistem, bukan bagian repo
. /etc/os-release
case " ${ID:-} ${ID_LIKE:-} " in
*debian*) ;;
*) die "distro '${PRETTY_NAME:-$(uname -s)}' bukan Debian; skrip ini khusus Debian 12/13" ;;
esac

id -u "$DEV_USER" >/dev/null 2>&1 ||
	die "user '$DEV_USER' belum ada; jalankan scripts/vps-bootstrap.sh dulu"

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
if [[ $DESKTOP == cinnamon ]]; then
	log "pasang Cinnamon + xrdp"
	apt-get install -y cinnamon cinnamon-core gnome-terminal dbus-x11 xrdp xorgxrdp
	SESSION_CMD="cinnamon-session"
else
	log "pasang XFCE + xrdp"
	apt-get install -y xfce4 xfce4-goodies dbus-x11 xrdp xorgxrdp
	SESSION_CMD="startxfce4"
fi

# .xsession: xrandr best-effort, lalu exec desktop. xorgxrdp menyesuaikan ukuran
# dengan klien; nilai --res hanya titik awal agar tampilan tidak terlalu kecil/besar.
cat >"/home/$DEV_USER/.xsession" <<EOF
# Dikelola scripts/vps-desktop.sh (docs/VPS.md, ADR-107).
xrandr --size $RES 2>/dev/null || xrandr -s $RES 2>/dev/null || true
exec $SESSION_CMD
EOF
chown "$DEV_USER:$DEV_USER" "/home/$DEV_USER/.xsession"
chmod 0755 "/home/$DEV_USER/.xsession"
log "~$DEV_USER/.xsession -> $SESSION_CMD (resolusi awal $RES)"

if [[ $DESKTOP == cinnamon ]]; then
	# Muffin selalu melakukan compositing; tidak ada sakelar mati total. Yang bisa
	# dilakukan: matikan efek dan unredirect jendela full-screen.
	if command -v dbus-run-session >/dev/null 2>&1 && command -v gsettings >/dev/null 2>&1; then
		runuser -u "$DEV_USER" -- env HOME="/home/$DEV_USER" dbus-run-session -- \
			gsettings set org.cinnamon desktop-effects false ||
			warn "tidak bisa mematikan efek Cinnamon (schema/key tidak tersedia)"
		runuser -u "$DEV_USER" -- env HOME="/home/$DEV_USER" dbus-run-session -- \
			gsettings set org.cinnamon.muffin unredirect-fullscreen-windows true ||
			warn "tidak bisa mengatur unredirect-fullscreen-windows"
		log "Cinnamon: efek dimatikan, full-screen di-unredirect (compositor tetap aktif)"
	else
		warn "dbus-run-session/gsettings tidak ada; atur compositor Cinnamon manual"
	fi
else
	conf="/home/$DEV_USER/.config/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml"
	install -d -m 0700 -o "$DEV_USER" -g "$DEV_USER" "$(dirname "$conf")"
	cat >"$conf" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfwm4" version="1.0">
  <property name="general" type="empty">
    <property name="use_compositing" type="bool" value="false"/>
  </property>
</channel>
EOF
	chown "$DEV_USER:$DEV_USER" "$conf"
	chmod 0600 "$conf"
	log "XFCE: compositor xfwm4 dimatikan"
fi

systemctl enable --now xrdp
log "xrdp aktif (port $XRDP_PORT)"

if command -v ufw >/dev/null 2>&1; then
	ufw default deny incoming
	ufw default allow outgoing
	ufw deny "$XRDP_PORT/tcp" comment 'RDP tidak pernah publik'
	# tailnet boleh RDP; rule ini disisipkan di atas DENY agar menang urutan.
	if ! ufw status 2>/dev/null | grep -qE "3389.*tailscale0|tailscale0.*3389"; then
		ufw insert 1 allow in on tailscale0 to any port "$XRDP_PORT" proto tcp ||
			warn "gagal menambah rule tailscale0; jalankan ulang setelah Tailscale aktif"
	fi
	ufw --force enable
	log "ufw: 3389 ditolak publik, diizinkan hanya di tailscale0"
else
	warn "ufw tidak ada; pastikan 3389 tidak terbuka ke publik"
fi

cat <<EOF

[desktop] Selesai. Langkah dari ponsel:

  1. Di VPS (sekali):
       sudo tailscale up
       tailscale ip -4        # atau pakai MagicDNS, mis. damnbot-vps
  2. Di ponsel: pasang aplikasi Tailscale (login akun yang sama) dan
     "Windows App" / Microsoft Remote Desktop.
  3. Tambah PC: host = IP tailnet atau nama MagicDNS, user = $DEV_USER,
     password = password user $DEV_USER.
  4. Di pengaturan sesi RDP pilih resolusi $RES. xorgxrdp mengambil ukuran
     dari klien; nilai --res hanyalah titik awal lewat xrandr.
  5. Verifikasi port tidak publik: sudo ufw status numbered
     (harus ada ALLOW ... tailscale0 di atas, DENY 3389 untuk publik).
  6. Cinnamon: Muffin selalu compositing; skrip mematikan efek dan
     meng-unredirect full-screen, bukan mematikan compositor sepenuhnya.
EOF
if ! command -v tailscale >/dev/null 2>&1; then
	warn "Tailscale belum terpasang; lihat https://tailscale.com/download/linux"
fi
