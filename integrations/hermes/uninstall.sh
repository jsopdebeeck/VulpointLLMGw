#!/usr/bin/env bash
# LLMGW by Vulpoint.com : retire le branchement de Hermes Agent sur la passerelle LLMGW.
#
#   integrations/hermes/uninstall.sh [--hermes-home <dossier>] [--backup <fichier>] [--dry-run]
#
#   --hermes-home  profil Hermes (défaut : celui qu'utilise « hermes », souvent ~/.hermes)
#   --backup       sauvegarde à restaurer ; défaut : config.yaml.bak-llmgw-<date> faite par la première
#                  installation depuis la dernière désinstallation (l'état d'avant le kit)
#   --dry-run      affiche ce qui serait fait sans rien écrire
#
# Ne supprime rien : config.yaml actuel conservé en config.yaml.bak-before-uninstall-<date>, plugin
# déplacé dans plugins/.disabled-llmgw-<date>. La clé reste dans le .env (hermes config unset LLMGW_API_KEY).
set -euo pipefail
kit=$(cd "$(dirname "$0")" && pwd)

home="" args=()
while [ $# -gt 0 ]; do
	case "$1" in
	--hermes-home) home="${2:?--hermes-home demande un dossier}"; shift 2 ;;
	--hermes-home=*) home="${1#*=}"; shift ;;
	--backup) args+=(--backup "${2:?--backup demande un fichier}"); shift 2 ;;
	--backup=*) args+=(--backup "${1#*=}"); shift ;;
	--dry-run) args+=("$1"); shift ;;
	-h | --help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
	*) echo "option inconnue : $1 (--help)" >&2; exit 2 ;;
	esac
done

hermes=${HERMES_BIN:-$(command -v hermes || true)}
[ -n "$hermes" ] && [ -x "$hermes" ] || { echo "Hermes Agent introuvable (commande « hermes »)" >&2; exit 1; }
if [ -n "$home" ]; then
	[ -d "$home" ] || { echo "dossier Hermes introuvable : $home" >&2; exit 1; }
	home=$(cd "$home" && pwd)
	export HERMES_HOME="$home"
	args+=(--hermes-home "$home")
fi

py=$("$hermes" --print-runtime-command 2>/dev/null | sed -n 's/^\["\([^"]*\)".*/\1/p' | head -n 1 || true)
[ -n "$py" ] && [ -x "$py" ] || py=$(command -v python3 || true)
[ -n "$py" ] || { echo "python3 introuvable" >&2; exit 1; }

exec "$py" -I "$kit/lib/hermes_setup.py" uninstall --hermes "$hermes" ${args[@]+"${args[@]}"}
