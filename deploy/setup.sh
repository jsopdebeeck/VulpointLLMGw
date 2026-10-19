#!/usr/bin/env bash
# LLMGW by Vulpoint.com : installation ou mise à jour sur un serveur Ubuntu 22.04 / 24.04 LTS
# ou Debian 12 (systemd). En root, depuis le dossier de l'archive llmgw-<version>-linux-<arch>.tar.gz.
#
#   sudo ./setup.sh [options]
#
#   --domain DOMAINE   HTTPS avec Caddy et Let's Encrypt (le DNS doit pointer vers ce serveur).
#   --email EMAIL      contact Let's Encrypt (facultatif).
#   --admin-ip IP      IP ou plage CIDR autorisée sur /dashboard et /admin (répétable), dans Caddy
#                      et dans config.toml. Sans elle : tunnel SSH seulement (en mise à jour, les IP
#                      déjà configurées dans Caddy sont gardées).
#   --config FICHIER   installe ce config.toml (l'actuel est gardé en config.toml.bak-setup-*).
#                      Sinon : généré au premier passage (jeton admin aléatoire), conservé ensuite.
#   --no-caddy         n'installe ni ne configure Caddy.
#   --no-firewall      ne touche pas au pare-feu ufw (fail2ban protège SSH dans tous les cas).
#   --yes              aucune question (obligatoire sans terminal).
#
# Relancer setup.sh avec une nouvelle archive = mise à jour : configuration, base et Caddy sont
# conservés ; le nouveau binaire valide la configuration avant de remplacer l'ancien (gardé en
# llmgw.prev, rétabli si le service ne redémarre pas). La configuration SSH n'est jamais modifiée.
set -Eeuo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

dir=/opt/llmgw
unit=/etc/systemd/system/llmgw.service
backups=/var/backups/llmgw
log=/var/log/llmgw-setup.log
marker="llmgw derrière Caddy"
here=$(cd "$(dirname "$0")" && pwd)

say() { printf '%s\n' "$*"; }
warn() { printf '! %s\n' "$*"; }
die() { printf '✗ %s\n' "$*" >&2; exit 1; }
usage() { awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; }
trap '[ "$BASH_SUBSHELL" != 0 ] || printf "✗ échec ligne %s : %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR

valid_domain() { [[ $1 =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ && $1 == *.* ]]; }
valid_email() { [[ $1 =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; }
valid_ip() { [[ $1 =~ ^[0-9A-Fa-f:.]+(/[0-9]{1,3})?$ && $1 =~ [.:] ]]; }
rand_hex() { od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'; }
toml_list() { local s="" x; for x in "$@"; do s="$s${s:+, }\"$x\""; done; printf '[%s]' "$s"; }
toml_get() { sed -n "s/^[[:space:]]*$2[[:space:]]*=[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" "$1" | head -1; }

# toml_set FICHIER SECTION CLÉ VALEUR : CLÉ = VALEUR dans [SECTION]. Remplace la clé active
# (en gardant son commentaire de fin de ligne), sinon l'ajoute sous son exemple commenté ou sous
# l'en-tête ; ajoute la section en fin de fichier si elle manque.
toml_set() {
	local f=$1 sec=$2 key=$3 val=$4
	grep -qE "^[[:space:]]*\[$sec\][[:space:]]*(#.*)?$" "$f" ||
		printf '\n# ajouté par setup.sh\n[%s]\n' "$sec" >>"$f"
	awk -v sec="$sec" -v key="$key" -v val="$val" '
		NR == FNR {
			if ($0 ~ /^[[:space:]]*\[/) {
				in_sec = ($0 ~ "^[[:space:]]*\\[" sec "\\][[:space:]]*(#.*)?$")
				if (in_sec && !h) h = FNR
				next
			}
			if (in_sec && !a && $0 ~ "^[[:space:]]*" key "[[:space:]]*=") a = FNR
			if (in_sec && !c && $0 ~ "^[[:space:]]*#[[:space:]]*" key "[[:space:]]*=") c = FNR
			next
		}
		skip { if ($0 ~ /\]/) skip = 0; next }
		FNR == a {
			line = key " = " val
			if ($0 ~ /=[[:space:]]*\[/ && $0 !~ /\]/) skip = 1
			else if (match($0, /[[:space:]]+#[^"]*$/)) {
				# commentaire gardé, à la même colonne si la place suffit
				com = substr($0, RSTART); col = RSTART + index(com, "#") - 1
				com = substr(com, index(com, "#"))
				line = line "  "
				while (length(line) < col - 1) line = line " "
				line = line com
			}
			print line
			next
		}
		{ print }
		!a && FNR == (c ? c : h) { print key " = " val }
	' "$f" "$f" >"$f.tmp"
	cat "$f.tmp" >"$f"
	rm -f "$f.tmp"
}

# adresse locale de llmgw d'après listen (0.0.0.0, [::] ou vide : 127.0.0.1)
upstream() {
	local l=${1:-127.0.0.1:8080} h
	h=${l%:*}
	case "$h" in "" | 0.0.0.0 | "[::]") echo "127.0.0.1:${l##*:}" ;; *) echo "$l" ;; esac
}
is_loopback() { case "${1%:*}" in 127.* | localhost | "[::1]") return 0 ;; *) return 1 ;; esac; }

# ---------------------------------------------------------------- options
domain="" email="" config="" caddy=1 firewall=1 yes=0
admin_ips=()
need() { [ -n "${2:-}" ] && [ "${2#--}" = "$2" ] || die "valeur manquante après $1"; }
while [ $# -gt 0 ]; do
	case "$1" in
	--domain) need "$@"; domain=$2; shift 2 ;;
	--email) need "$@"; email=$2; shift 2 ;;
	--admin-ip) need "$@"; admin_ips+=("$2"); shift 2 ;;
	--config) need "$@"; config=$2; shift 2 ;;
	--no-caddy) caddy=0; shift ;;
	--no-firewall) firewall=0; shift ;;
	-y | --yes) yes=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) die "option inconnue : $1 (--help)" ;;
	esac
done
[ -z "$domain" ] || valid_domain "$domain" || die "domaine invalide : $domain"
[ -z "$email" ] || valid_email "$email" || die "email invalide : $email"
for ip in ${admin_ips[@]+"${admin_ips[@]}"}; do valid_ip "$ip" || die "IP ou plage CIDR invalide : $ip"; done
[ -z "$domain" ] || [ $caddy = 1 ] || die "--domain et --no-caddy sont incompatibles"
if [ -n "$config" ]; then
	[ -f "$config" ] && [ -r "$config" ] || die "$config introuvable"
	config=$(cd "$(dirname "$config")" && pwd)/$(basename "$config")
fi

# ---------------------------------------------------------------- vérifications
[ "$(id -u)" = 0 ] || die "à lancer en root : sudo $0"
[ -d /run/systemd/system ] && command -v systemctl >/dev/null || die "systemd requis (absent ou pas en service)"
[ -r /etc/os-release ] || die "/etc/os-release introuvable : système non pris en charge"
os=$(. /etc/os-release && echo "${ID:-}:${VERSION_ID:-}")
os_name=$(. /etc/os-release && echo "${PRETTY_NAME:-$os}")
case "$os" in
ubuntu:22.04 | ubuntu:24.04 | debian:12) ;;
*) die "système non pris en charge : $os_name (Ubuntu 22.04 / 24.04 LTS ou Debian 12 attendus)" ;;
esac
for f in llmgw config.example.toml llmgw.service Caddyfile; do
	[ -f "$here/$f" ] || die "$here/$f manquant : lancer setup.sh depuis le dossier de l'archive"
done
# architecture du binaire (en-tête ELF) contre celle du serveur, sans l'exécuter
hdr=$(od -An -tx1 -N20 "$here/llmgw" | tr -d ' \n')
case "${hdr:0:8}:${hdr:36:4}" in
7f454c46:3e00) bin_arch=x86_64 ;;
7f454c46:b700) bin_arch=aarch64 ;;
*) die "$here/llmgw n'est pas un binaire Linux amd64 / arm64" ;;
esac
srv_arch=$(uname -m)
if [ "$bin_arch" != "$srv_arch" ]; then
	want=amd64
	[ "$srv_arch" = aarch64 ] && want=arm64
	die "binaire $bin_arch, serveur $srv_arch : prendre l'archive llmgw-…-linux-$want.tar.gz"
fi
version=$("$here/llmgw" -version 2>/dev/null | awk '{print $NF}') || true
[ -n "$version" ] || { version=${here##*/}; version=${version#llmgw-}; }

# ---------------------------------------------------------------- état actuel
upgrade=0
[ -f "$dir/config.toml" ] && upgrade=1
our_caddy=0 prev_domain="" prev_ips="" prev_email=""
if [ -f /etc/caddy/Caddyfile ] && grep -q "$marker" /etc/caddy/Caddyfile; then
	our_caddy=1
	prev_domain=$(sed -n 's/^\([A-Za-z0-9.-]*\) {$/\1/p' /etc/caddy/Caddyfile | head -1)
	prev_ips=$(sed -n 's/^[[:space:]]*not remote_ip[[:space:]]*//p' /etc/caddy/Caddyfile | head -1)
	prev_email=$(sed -n 's/^[[:space:]]*email[[:space:]][[:space:]]*\([^[:space:]]*\).*/\1/p' /etc/caddy/Caddyfile | head -1)
	valid_domain "$prev_domain" || prev_domain=""
	valid_email "$prev_email" || prev_email=""
	for ip in $prev_ips; do valid_ip "$ip" || prev_ips=""; done
fi

if [ $yes = 0 ]; then
	[ -t 0 ] || die "pas de terminal : relancer avec --yes"
	if [ $caddy = 1 ] && [ -z "$domain" ] && [ $our_caddy = 0 ]; then
		read -r -p "Domaine pour HTTPS (Caddy, Let's Encrypt) ; vide = sans Caddy : " domain
		if [ -n "$domain" ]; then
			valid_domain "$domain" || die "domaine invalide : $domain"
			[ -n "$email" ] || read -r -p "Email de contact Let's Encrypt (facultatif) : " email
			[ -z "$email" ] || valid_email "$email" || die "email invalide : $email"
			if [ ${#admin_ips[@]} -eq 0 ]; then
				read -r -p "IP autorisées sur le dashboard (séparées par des espaces ; vide = tunnel SSH) : " ans
				for ip in $ans; do valid_ip "$ip" || die "IP invalide : $ip"; admin_ips+=("$ip"); done
			fi
		fi
	fi
fi

# Caddy : à (re)configurer (--domain) ou déjà en place (mise à jour sans --domain)
ips_given=${#admin_ips[@]}
caddy_conf=0 caddy_use=""
if [ $caddy = 1 ]; then
	if [ -n "$domain" ]; then
		caddy_conf=1 caddy_use=1
	elif [ $our_caddy = 1 ]; then
		caddy_use=1 domain=$prev_domain
	fi
	[ -n "$email" ] || email=$prev_email
	if [ ${#admin_ips[@]} -eq 0 ] && [ $our_caddy = 1 ] && [ -n "$prev_ips" ] && [ "$prev_ips" != 127.0.0.1/32 ]; then
		read -r -a admin_ips <<<"$prev_ips"
	fi
fi
caddy_ips=${admin_ips[*]:-127.0.0.1/32}

say "LLMGW $version ($bin_arch) sur $os_name"
if [ -n "$config" ]; then
	say "  llmgw     : config.toml remplacé par $config"
elif [ $upgrade = 1 ]; then
	say "  llmgw     : mise à jour, config.toml conservé"
else
	say "  llmgw     : installation dans $dir, config.toml généré"
fi
if [ $caddy_conf = 1 ]; then
	say "  HTTPS     : Caddy pour $domain, contact Let's Encrypt : ${email:-aucun}"
elif [ -n "$caddy_use" ]; then
	say "  HTTPS     : Caddy déjà configuré (${domain:-?}), inchangé"
else
	say "  HTTPS     : non (pas de --domain)"
fi
say "  admin     : ${admin_ips[*]:-tunnel SSH seulement}"
if [ $firewall = 1 ]; then say "  pare-feu  : ufw (SSH${caddy_use:+, 80, 443})"; else say "  pare-feu  : non modifié"; fi
if [ $yes = 0 ]; then
	read -r -p "Continuer ? [o/N] " ans
	case "$ans" in [oOyY]*) ;; *) die "abandon, rien n'a été modifié" ;; esac
fi

# ---------------------------------------------------------------- paquets système
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 NEEDRESTART_MODE=l
apt_() {
	apt-get -q -y -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef \
		-o Dpkg::Options::=--force-confold "$@" >>"$log" 2>&1 ||
		{ tail -n 20 "$log" >&2; die "apt-get $1 a échoué (journal complet : $log)"; }
}
say "→ paquets système (journal : $log)"
echo "---- setup.sh $version, $(date '+%F %T')" >>"$log"
apt_ update
apt_ install ca-certificates curl ufw unattended-upgrades sqlite3 fail2ban python3-systemd

# mises à jour de sécurité automatiques, sans question
cat >/etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
// llmgw setup.sh : mises à jour automatiques quotidiennes (origines : 50unattended-upgrades)
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
# Ubuntu se limite déjà à -security ; Debian ajoute par défaut les mises à jour de version mineure
if [ "${os%%:*}" = debian ]; then
	cat >/etc/apt/apt.conf.d/52llmgw-unattended-upgrades <<'EOF'
// llmgw setup.sh : seulement les mises à jour de sécurité de Debian
#clear Unattended-Upgrade::Origins-Pattern;
Unattended-Upgrade::Origins-Pattern {
	"origin=Debian,codename=${distro_codename}-security,label=Debian-Security";
	"origin=Debian,codename=${distro_codename},label=Debian-Security";
};
EOF
fi
systemctl enable --quiet --now unattended-upgrades 2>/dev/null || warn "unattended-upgrades : service non activé"
say "✓ paquets, mises à jour de sécurité automatiques"

if [ $caddy_conf = 1 ] && ! command -v caddy >/dev/null; then
	if ! apt-cache policy caddy 2>/dev/null | grep -q 'Candidate: [0-9]'; then
		# dépôt officiel (caddyserver.com/docs/install#debian-ubuntu-raspbian) ; debian-keyring et
		# apt-transport-https de la doc sont inutiles ici, gnupg suffit pour la clé
		say "→ dépôt apt officiel de Caddy"
		apt_ install gnupg
		curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key |
			gpg --batch --yes --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
		echo "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main" \
			>/etc/apt/sources.list.d/caddy-stable.list
		chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
		apt_ update
	fi
	say "→ installation de Caddy"
	apt_ install caddy
	say "✓ $(caddy version | awk '{print "Caddy", $1}')"
fi

# ---------------------------------------------------------------- utilisateur et dossier
if ! id llmgw >/dev/null 2>&1; then
	if getent group llmgw >/dev/null; then grp=(-g llmgw); else grp=(--user-group); fi
	useradd --system "${grp[@]}" --home-dir "$dir" --no-create-home --shell /usr/sbin/nologin llmgw
	say "✓ utilisateur système llmgw"
fi
install -d -o llmgw -g llmgw -m 700 "$dir"

# ---------------------------------------------------------------- sauvegarde quotidienne
# exécutée en tant que llmgw : sqlite3 ne crée pas de fichiers -wal / -shm appartenant à root
install -d -o llmgw -g llmgw -m 700 "$backups"
cat >/usr/local/sbin/llmgw-backup <<'EOF'
#!/bin/sh
# llmgw : sauvegarde de gateway.db (copie cohérente, sqlite3 .backup) et de config.toml dans
# /var/backups/llmgw. Lancée chaque nuit par llmgw-backup.timer, en tant que llmgw.
# Les sauvegardes de plus de 14 jours sont supprimées.
set -eu
umask 077
dir=/opt/llmgw dest=/var/backups/llmgw day=$(date +%F)
db=$(sed -n 's/^[[:space:]]*db[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$dir/config.toml" | head -1)
case "${db:=gateway.db}" in /*) ;; *) db=$dir/$db ;; esac
[ ! -f "$db" ] || sqlite3 "$db" ".timeout 10000" ".backup '$dest/gateway-$day.db'"
cp "$dir/config.toml" "$dest/config-$day.toml"
find "$dest" -maxdepth 1 -type f \( -name 'gateway-*.db' -o -name 'config-*.toml' \) -mtime +14 -delete
echo "sauvegarde $day dans $dest"
EOF
chmod 755 /usr/local/sbin/llmgw-backup
cat >/etc/systemd/system/llmgw-backup.service <<EOF
# llmgw : sauvegarde de gateway.db et config.toml (installé par setup.sh)
[Unit]
Description=llmgw, sauvegarde de gateway.db et config.toml

[Service]
Type=oneshot
User=llmgw
Group=llmgw
ExecStart=/usr/local/sbin/llmgw-backup
UMask=0077
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$dir $backups
EOF
cat >/etc/systemd/system/llmgw-backup.timer <<'EOF'
# llmgw : sauvegarde quotidienne (installé par setup.sh)
[Unit]
Description=llmgw, sauvegarde quotidienne

[Timer]
OnCalendar=*-*-* 03:30:00
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --quiet --now llmgw-backup.timer
# mise à jour : une copie de la base avant de changer de binaire
if [ $upgrade = 1 ]; then
	if systemctl start llmgw-backup.service; then
		say "✓ sauvegarde avant mise à jour dans $backups"
	else
		warn "sauvegarde avant mise à jour impossible (journalctl -u llmgw-backup)"
	fi
fi
say "✓ sauvegarde quotidienne ($backups, 14 jours)"

# ---------------------------------------------------------------- configuration
newcfg="" token=""
if [ -n "$config" ]; then
	install -o llmgw -g llmgw -m 600 "$config" "$dir/config.toml.new"
	newcfg=given
elif [ $upgrade = 0 ]; then
	f=$dir/config.toml.new
	install -o llmgw -g llmgw -m 600 "$here/config.example.toml" "$f"
	token=$(rand_hex 24)
	toml_set "$f" server admin_token "\"$token\""
	if [ -n "$caddy_use" ]; then
		toml_set "$f" server listen '"127.0.0.1:8080"'
		toml_set "$f" security trusted_proxies '["127.0.0.1/32", "::1/128"]'
		[ -z "$domain" ] || toml_set "$f" auth base_url "\"https://$domain\""
	fi
	if [ ${#admin_ips[@]} -gt 0 ]; then
		# 127.0.0.1 et ::1 : le tunnel SSH reste possible
		toml_set "$f" security admin_allow "$(toml_list 127.0.0.1/32 ::1/128 "${admin_ips[@]}")"
	fi
	# clés d'exemple (gw-alice-remplace-moi…) : rendues aléatoires, jamais publiques
	n=$(grep -cE '^[[:space:]]*key[[:space:]]*=[[:space:]]*"[^"]*remplace-moi"' "$f" || true)
	toks="" i=0
	while [ "$i" -lt "${n:-0}" ]; do toks="$toks $(rand_hex 16)"; i=$((i + 1)); done
	awk -v toks="$toks" 'BEGIN { split(toks, t, " ") }
		/^[[:space:]]*key[[:space:]]*=[[:space:]]*"[^"]*remplace-moi"/ { sub(/remplace-moi"/, t[++k] "\"") }
		{ print }' "$f" >"$f.tmp"
	cat "$f.tmp" >"$f"
	rm -f "$f.tmp"
	newcfg=generated
fi

# lu par systemd (root) avant de lancer le service : reste en root:root 600
if [ ! -f "$dir/secrets.env" ]; then
	install -o root -g root -m 600 /dev/null "$dir/secrets.env"
	cat >"$dir/secrets.env" <<'EOF'
# llmgw : clés des LLM cibles et autres secrets, une variable par ligne, sans guillemets.
# Lu par systemd au démarrage : après modification, systemctl restart llmgw
# Noms attendus : api_key_env / client_secret_env / smtp_password_env de config.toml.
#MISTRAL_API_KEY=
#ANTHROPIC_API_KEY=
#OPENAI_API_KEY=
EOF
	say "✓ $dir/secrets.env créé (à compléter)"
fi

# ---------------------------------------------------------------- binaire
install -o llmgw -g llmgw -m 755 "$here/llmgw" "$dir/llmgw.new"
check=$dir/config.toml
[ -z "$newcfg" ] || check=$dir/config.toml.new
if ! out=$(runuser -u llmgw -- "$dir/llmgw.new" -config "$check" -check 2>&1); then
	printf '%s\n' "$out" >&2
	rm -f "$dir/llmgw.new" "$dir/config.toml.new"
	die "configuration refusée par le nouveau binaire : llmgw n'a pas été modifié"
fi
say "✓ $out"
version=$("$dir/llmgw.new" -version | awk '{print $NF}')
if [ -f "$dir/llmgw" ]; then mv -f "$dir/llmgw" "$dir/llmgw.prev"; fi
mv -f "$dir/llmgw.new" "$dir/llmgw"
cfg_bak=""
if [ -n "$newcfg" ]; then
	if [ -f "$dir/config.toml" ]; then
		cfg_bak=$dir/config.toml.bak-setup-$(date +%Y%m%d-%H%M%S)
		cp -p "$dir/config.toml" "$cfg_bak"
		say "✓ ancien config.toml gardé : $cfg_bak"
	fi
	mv -f "$dir/config.toml.new" "$dir/config.toml"
fi
if [ -n "$token" ]; then
	install -o root -g root -m 600 /dev/null "$dir/admin_token.txt"
	echo "$token" >"$dir/admin_token.txt"
fi

listen=$(toml_get "$dir/config.toml" listen)
listen=${listen:-127.0.0.1:8080}
up=$(upstream "$listen")
port=${listen##*:}

# ---------------------------------------------------------------- service
install -m 644 "$here/llmgw.service" "$unit"
systemctl daemon-reload
systemctl enable --quiet llmgw
# échec de démarrage traité ci-dessous (retour arrière), pas par set -e
systemctl restart llmgw || true
started=0
for i in $(seq 1 15); do
	sleep 1
	systemctl is-active --quiet llmgw || continue
	if curl -fsS --max-time 2 "http://$up/version" 2>/dev/null | grep -q "\"$version\""; then started=1; break; fi
done
if [ $started = 0 ]; then
	journalctl -u llmgw -n 20 --no-pager >&2 || true
	if [ -f "$dir/llmgw.prev" ]; then
		mv -f "$dir/llmgw.prev" "$dir/llmgw"
		msg="retour à la version précédente"
		if [ -n "$cfg_bak" ]; then
			cp -p "$cfg_bak" "$dir/config.toml"
			msg="$msg et à l'ancien config.toml"
		fi
		systemctl restart llmgw || true
		die "le nouveau binaire ne démarre pas : $msg"
	fi
	[ -z "$token" ] || warn "jeton admin : $dir/admin_token.txt"
	die "llmgw ne démarre pas ou ne répond pas sur $up (journal ci-dessus)"
fi
say "✓ llmgw $version actif sur $listen (PID $(systemctl show -p MainPID --value llmgw))"

# ---------------------------------------------------------------- pare-feu et fail2ban
ssh_ports=$({
	sshd -T 2>/dev/null | awk '$1 == "port" { print $2 }'
	grep -hsiE '^[[:space:]]*Port[[:space:]]+[0-9]+' /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf | awk '{ print $2 }'
	[ -z "${SSH_CONNECTION:-}" ] || echo "${SSH_CONNECTION##* }"
	echo 22
} | grep -E '^[0-9]+$' | sort -nu | tr '\n' ' ')
ssh_ports=${ssh_ports% }
if [ $firewall = 1 ]; then
	# SSH d'abord : ufw n'est activé que si toutes les règles SSH sont en place
	ssh_ok=1
	ufw allow OpenSSH >/dev/null 2>&1 || ufw allow 22/tcp >/dev/null || ssh_ok=0
	for p in $ssh_ports; do
		[ "$p" = 22 ] || ufw allow "$p/tcp" >/dev/null || ssh_ok=0
	done
	opened="SSH ($ssh_ports)"
	if [ -n "$caddy_use" ]; then
		ufw allow 80/tcp >/dev/null && ufw allow 443/tcp >/dev/null && opened="$opened, 80, 443"
	elif ! is_loopback "$listen"; then
		ufw allow "$port/tcp" >/dev/null && opened="$opened, $port (llmgw)"
	fi
	if [ $ssh_ok = 0 ]; then
		warn "règle SSH impossible : ufw n'est pas activé"
	elif ufw --force enable >/dev/null; then
		say "✓ pare-feu ufw actif : $opened"
	else
		warn "ufw ne s'active pas (VPS sans iptables ?) : pare-feu à prévoir chez l'hébergeur"
	fi
fi
cat >/etc/fail2ban/jail.d/llmgw-sshd.local <<EOF
# llmgw setup.sh : protection SSH, bannissement après des échecs de connexion répétés
[sshd]
enabled = true
port = ${ssh_ports// /,}
backend = systemd
EOF
if systemctl enable --quiet fail2ban && systemctl restart fail2ban; then
	say "✓ fail2ban : SSH protégé"
else
	warn "fail2ban ne démarre pas (journalctl -u fail2ban)"
fi

# ---------------------------------------------------------------- Caddy
https_ok=""
if [ $caddy_conf = 1 ]; then
	new=/etc/caddy/Caddyfile.llmgw-new
	awk -v dom="$domain" -v ips="$caddy_ips" -v email="$email" -v up="$up" '
		/^LLMGW_DOMAIN[[:space:]]*\{/ && email != "" { printf "{\n\temail %s\n}\n\n", email }
		!/^[[:space:]]*#/ {
			gsub(/LLMGW_DOMAIN/, dom)
			gsub(/ADMIN_IPS/, ips)
			sub(/reverse_proxy 127\.0\.0\.1:8080/, "reverse_proxy " up)
		}
		{ print }' "$here/Caddyfile" >"$new"
	chmod 644 "$new"
	if ! out=$(caddy validate --adapter caddyfile --config "$new" 2>&1); then
		printf '%s\n' "$out" >&2
		rm -f "$new"
		die "Caddyfile refusé par caddy validate : Caddy n'a pas été modifié"
	fi
	if [ -f /etc/caddy/Caddyfile ] && ! grep -q "$marker" /etc/caddy/Caddyfile; then
		bak=/etc/caddy/Caddyfile.bak-$(date +%Y%m%d-%H%M%S)
		cp -p /etc/caddy/Caddyfile "$bak"
		warn "ancien /etc/caddy/Caddyfile gardé : $bak (les sites qu'il servait ne le sont plus)"
	fi
	mv -f "$new" /etc/caddy/Caddyfile
	# caddy validate (root) a pu créer le fichier journal : Caddy tourne en tant que caddy
	install -d -o caddy -g caddy /var/log/caddy
	chown -R caddy:caddy /var/log/caddy
	systemctl enable --quiet caddy
	if ! systemctl reload-or-restart caddy; then
		journalctl -u caddy -n 20 --no-pager >&2 || true
		die "Caddy ne démarre pas (journal ci-dessus)"
	fi
	say "✓ Caddy configuré pour $domain"
	say "→ attente du certificat HTTPS (40 s au plus)"
	for i in $(seq 1 20); do
		if curl -fsS --max-time 3 --resolve "$domain:443:127.0.0.1" "https://$domain/version" >/dev/null 2>&1; then
			https_ok=1
			break
		fi
		sleep 2
	done
	if [ -n "$https_ok" ]; then
		say "✓ https://$domain répond"
	else
		warn "pas encore de certificat : vérifier que le DNS de $domain pointe ici et que 80/443 sont"
		warn "ouverts chez l'hébergeur ; Caddy réessaie seul (journalctl -u caddy -f)"
	fi
fi

# ---------------------------------------------------------------- résumé
say ""
say "LLMGW $version installé."
if [ -n "$caddy_use" ] && [ -n "$domain" ]; then
	say "  API (clients)   : https://$domain/v1   (OpenAI : /v1/chat/completions ; Anthropic : /v1/messages)"
	say "  portail         : https://$domain/chat"
	admin_url=https://$domain/dashboard
elif ! is_loopback "$listen"; then
	say "  API (clients)   : http://<IP du serveur>:$port/v1   (HTTP en clair : préférer --domain)"
	admin_url="http://<IP du serveur>:$port/dashboard"
else
	say "  llmgw écoute sur $listen : joignable depuis le serveur seulement. Pour l'exposer : --domain"
	say "  (HTTPS), ou listen = \"0.0.0.0:$port\" dans $dir/config.toml puis relancer setup.sh."
	admin_url=""
fi
ssh_dest="${SUDO_USER:-root}@${domain:-<serveur>}"
if [ ${#admin_ips[@]} -gt 0 ] && [ -n "$admin_url" ]; then
	say "  dashboard       : $admin_url   (depuis ${admin_ips[*]})"
else
	say "  dashboard       : par tunnel SSH, depuis votre poste :"
	say "                      ssh -L $port:127.0.0.1:$port $ssh_dest"
	say "                    puis http://127.0.0.1:$port/dashboard"
fi
if [ -n "$token" ]; then
	say "  jeton admin     : $token"
	say "                    (affiché une seule fois ; copie dans $dir/admin_token.txt, root seulement)"
elif [ -f "$dir/admin_token.txt" ]; then
	say "  jeton admin     : admin_token de $dir/config.toml (jeton initial : $dir/admin_token.txt)"
else
	say "  jeton admin     : admin_token de $dir/config.toml"
fi
if grep -qE '^[[:space:]]*admin_token[[:space:]]*=[[:space:]]*"(change-moi)?"' "$dir/config.toml"; then
	warn "admin_token vide ou par défaut dans config.toml : le changer avant d'exposer le serveur"
fi
if [ -z "$newcfg" ] && [ $upgrade = 1 ] && { [ $caddy_conf = 1 ] || [ "$ips_given" -gt 0 ]; }; then
	warn "config.toml conservé : vérifier [security] (trusted_proxies, admin_allow), listen et [auth] base_url"
fi
if [ -n "$caddy_use" ] && ! is_loopback "$listen"; then
	warn "listen = \"$listen\" derrière Caddy : préférer 127.0.0.1:$port (llmgw sans HTTPS sinon)"
fi
say "  sauvegardes     : $backups (chaque nuit, 14 jours)"
say "  journal         : journalctl -u llmgw -f${caddy_use:+ ; Caddy : /var/log/caddy/llmgw.log}"
say ""
say "Étapes suivantes :"
say "  1. clés des LLM dans $dir/secrets.env (MISTRAL_API_KEY=…), puis : systemctl restart llmgw"
say "  2. dashboard : cibles, profils et utilisateurs (clés gateway des clients)"
if sshd -T 2>/dev/null | grep -qi '^passwordauthentication yes'; then
	say "  3. SSH accepte encore les mots de passe : après avoir vérifié votre clé, mettre"
	say "     PasswordAuthentication no dans /etc/ssh/sshd_config (setup.sh ne touche pas à SSH)"
else
	say "  3. SSH : garder la connexion par clé seulement (setup.sh ne touche pas à SSH)"
fi
exit 0
