#!/usr/bin/env bash
# llmgw : construit l'archive Linux (build.sh), la copie sur un VPS (Ubuntu 22.04 / 24.04 LTS,
# Debian 12) et y lance setup.sh : installation au premier passage, mise à jour ensuite.
#
#   deploy/deploy.sh [options] utilisateur@serveur [-- options de setup.sh]
#
#   --config FICHIER   envoie ce config.toml (l'ancien est gardé en config.toml.bak-setup-*).
#                      Sans cette option : généré au premier déploiement (jeton admin aléatoire,
#                      affiché à la fin), puis celui du serveur (modifié depuis le dashboard) est conservé.
#   --domain DOMAINE   installe Caddy (HTTPS Let's Encrypt) pour ce domaine.
#   --email EMAIL      contact Let's Encrypt (facultatif).
#   --admin-ip IP      IP ou plage CIDR autorisée sur /dashboard et /admin (répétable).
#                      Par défaut : aucune, accès par tunnel SSH seulement.
#   --arch amd64|arm64 architecture du serveur (détectée sinon).
#   --skip-tests       ne lance pas go vet / go test avant de compiler.
#   -- …               options passées telles quelles à setup.sh (--no-caddy, --no-firewall).
#
# Mise à jour : deploy/deploy.sh utilisateur@serveur (configuration, base et Caddy conservés).
# Le serveur reçoit /opt/llmgw/{llmgw,config.toml,secrets.env,gateway.db} et les services
# llmgw, llmgw-backup.timer ; les clés des LLM vont dans secrets.env. Détail : setup.sh --help.
set -euo pipefail

cd "$(dirname "$0")/.."

host="" arch="" config="" domain="" email="" tests=1
admin_ips=() extra=()
need() { [ -n "${2:-}" ] || { echo "valeur manquante après $1" >&2; exit 2; }; }
while [ $# -gt 0 ]; do
	case "$1" in
	--config) need "$@"; config="$2"; shift 2 ;;
	--domain) need "$@"; domain="$2"; shift 2 ;;
	--email) need "$@"; email="$2"; shift 2 ;;
	--admin-ip) need "$@"; admin_ips+=("$2"); shift 2 ;;
	--arch) need "$@"; arch="$2"; shift 2 ;;
	--skip-tests) tests=0; shift ;;
	--) shift; extra=("$@"); break ;;
	-h | --help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
	-*) echo "option inconnue : $1" >&2; exit 2 ;;
	*) host="$1"; shift ;;
	esac
done
[ -n "$host" ] || { echo "usage : $0 [options] utilisateur@serveur (--help)" >&2; exit 2; }
[ -z "$config" ] || [ -f "$config" ] || { echo "$config introuvable" >&2; exit 2; }
case "$arch" in "" | amd64 | arm64) ;; *) echo "--arch amd64 ou arm64" >&2; exit 2 ;; esac

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

build=(--linux-only)
[ "$tests" = 1 ] || build+=(--skip-tests)
./build.sh "${build[@]}"
version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' main.go)
tarball=dist/llmgw-$version-linux-$arch.tar.gz
[ -f "$tarball" ] || { echo "$tarball introuvable" >&2; exit 1; }

stage=$(ssh_ "$host" mktemp -d)
echo "→ copie de $(basename "$tarball") vers $host:$stage"
scp_ "$tarball" "$host:$stage/"

args=(--yes)
[ -z "$domain" ] || args+=(--domain "$domain")
[ -z "$email" ] || args+=(--email "$email")
for ip in ${admin_ips[@]+"${admin_ips[@]}"}; do args+=(--admin-ip "$ip"); done
if [ -n "$config" ]; then
	scp_ "$config" "$host:$stage/config.toml"
	args+=(--config "$stage/config.toml")
fi
args+=(${extra[@]+"${extra[@]}"})

sudo=sudo
[ "$(ssh_ "$host" id -u)" = 0 ] && sudo=""
q_stage=$(printf %q "$stage")
remote="cd $q_stage && tar xzf $(printf %q "$(basename "$tarball")") &&"
remote="$remote $sudo bash $(printf %q "llmgw-$version/setup.sh") $(printf '%q ' "${args[@]}")"
# -t : sudo peut demander le mot de passe ; le dossier de transit est supprimé dans tous les cas
echo "→ setup.sh sur $host"
ssh_ -t "$host" "$remote; rc=\$?; rm -rf $q_stage; exit \$rc"
