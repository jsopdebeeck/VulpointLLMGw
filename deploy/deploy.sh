#!/usr/bin/env bash
# llmgw : compile, copie et (re)démarre le gateway sur un VPS Linux (Debian/Ubuntu, systemd).
#
#   deploy/deploy.sh [options] utilisateur@serveur
#
#   --config FICHIER   envoie ce config.toml (l'ancien est gardé en .bak-deploy-*).
#                      Obligatoire au premier déploiement ; ensuite, sans cette option,
#                      la configuration du serveur (modifiée depuis le dashboard) est conservée.
#   --domain DOMAINE   installe Caddy (HTTPS Let's Encrypt) pour ce domaine.
#   --admin-ip IP      IP ou plage CIDR autorisée sur /dashboard et /admin (répétable).
#                      Par défaut : aucune, accès par tunnel SSH seulement.
#   --arch amd64|arm64 architecture du serveur (détectée sinon).
#   --skip-tests       ne lance pas go test avant de compiler.
#
# Le serveur reçoit : /opt/llmgw/{llmgw,config.toml,secrets.env,gateway.db}
# et /etc/systemd/system/llmgw.service. Les clés des LLM vont dans secrets.env.
set -euo pipefail

cd "$(dirname "$0")/.."
here=deploy

host="" arch="" config="" domain="" tests=1
admin_ips=()
while [ $# -gt 0 ]; do
	case "$1" in
	--config) config="$2"; shift 2 ;;
	--domain) domain="$2"; shift 2 ;;
	--admin-ip) admin_ips+=("$2"); shift 2 ;;
	--arch) arch="$2"; shift 2 ;;
	--skip-tests) tests=0; shift ;;
	-h | --help) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	-*) echo "option inconnue : $1" >&2; exit 2 ;;
	*) host="$1"; shift ;;
	esac
done
[ -n "$host" ] || { echo "usage : $0 [options] utilisateur@serveur (--help)" >&2; exit 2; }
[ -z "$config" ] || [ -f "$config" ] || { echo "$config introuvable" >&2; exit 2; }

# une seule connexion SSH pour tout le script (un seul mot de passe / une seule clé)
ctl=$(mktemp -d)
trap 'ssh -o ControlPath="$ctl/s" -O exit "$host" 2>/dev/null; rm -rf "$ctl"' EXIT
ssh_() { ssh -o ControlMaster=auto -o ControlPath="$ctl/s" -o ControlPersist=120 "$@"; }
scp_() { scp -q -o ControlPath="$ctl/s" "$@"; }

echo "→ connexion à $host"
ssh_ "$host" true

if [ -z "$arch" ]; then
	case "$(ssh_ "$host" uname -m)" in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) echo "architecture non gérée, préciser --arch" >&2; exit 2 ;;
	esac
fi

if [ "$tests" = 1 ]; then
	echo "→ go vet et go test"
	go vet ./...
	go test ./...
fi
bin=dist/llmgw-linux-$arch
echo "→ compilation $bin"
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$bin" .

if [ -n "$domain" ]; then
	ips="${admin_ips[*]:-127.0.0.1/32}"
	sed -e "s|LLMGW_DOMAIN|$domain|" -e "s|ADMIN_IPS|$ips|" "$here/Caddyfile" >"$ctl/Caddyfile"
fi

stage=$(ssh_ "$host" mktemp -d)
echo "→ copie vers $host:$stage"
files=("$bin" "$here/llmgw.service" "$here/remote-install.sh")
[ -n "$config" ] && cp "$config" "$ctl/config.toml" && files+=("$ctl/config.toml")
[ -n "$domain" ] && files+=("$ctl/Caddyfile")
scp_ "${files[@]}" "$host:$stage/"

sudo=sudo
[ "$(ssh_ "$host" id -u)" = 0 ] && sudo=""
# -t : sudo peut demander le mot de passe
ssh_ -t "$host" "$sudo bash $stage/remote-install.sh $stage $(basename "$bin") ${domain:+--caddy}"
