#!/usr/bin/env bash
# LLMGW by Vulpoint.com : branche Hermes Agent sur la passerelle LLMGW.
#
#   integrations/hermes/install.sh --url <url> --key <gw-…> [--model <modèle>] [--warn] [--no-plugin]
#                                  [--keep-moa] [--dry-run] [--hermes-home <dossier>]
#
#   --url          adresse de la passerelle (ex. http://127.0.0.1:8088 ; /v1 facultatif)
#   --key          clé de la passerelle ; à défaut la variable LLMGW_API_KEY, sinon elle est demandée
#   --model        modèle principal, parmi ceux que la passerelle autorise pour la clé (GET /v1/models)
#   --warn         plugin en mode warn : les appels hors passerelle sont signalés, pas refusés
#   --no-plugin    configuration seule, sans le plugin llmgw
#   --keep-moa     MoA reste actif (ses modèles passent par la passerelle) ; sinon son fan-out est coupé
#   --dry-run      affiche les changements prévus sans rien écrire
#   --hermes-home  profil Hermes à configurer (défaut : celui qu'utilise « hermes », souvent ~/.hermes)
#
# Avant d'écrire : sauvegarde config.yaml.bak-llmgw-<date>. La clé va dans le .env du profil (600),
# config.yaml n'en contient que la référence ${LLMGW_API_KEY}. Relançable sans risque.
# Retour arrière : integrations/hermes/uninstall.sh.
set -euo pipefail
kit=$(cd "$(dirname "$0")" && pwd)

url="" key="${LLMGW_API_KEY:-}" home="" args=()
while [ $# -gt 0 ]; do
	case "$1" in
	--url) url="${2:?--url demande une valeur}"; shift 2 ;;
	--url=*) url="${1#*=}"; shift ;;
	--key) key="${2:?--key demande une valeur}"; shift 2 ;;
	--key=*) key="${1#*=}"; shift ;;
	--model) args+=(--model "${2:?--model demande une valeur}"); shift 2 ;;
	--model=*) args+=(--model "${1#*=}"); shift ;;
	--hermes-home) home="${2:?--hermes-home demande un dossier}"; shift 2 ;;
	--hermes-home=*) home="${1#*=}"; shift ;;
	--warn | --no-plugin | --keep-moa | --dry-run) args+=("$1"); shift ;;
	-h | --help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
	*) echo "option inconnue : $1 (--help)" >&2; exit 2 ;;
	esac
done
[ -n "$url" ] || { echo "--url est obligatoire (--help)" >&2; exit 2; }

hermes=${HERMES_BIN:-$(command -v hermes || true)}
[ -n "$hermes" ] && [ -x "$hermes" ] || {
	echo "Hermes Agent introuvable (commande « hermes ») : installez-le d'abord, ou HERMES_BIN=/chemin/hermes" >&2
	exit 1
}
if [ -n "$home" ]; then
	[ -d "$home" ] || { echo "dossier Hermes introuvable : $home" >&2; exit 1; }
	home=$(cd "$home" && pwd)
	export HERMES_HOME="$home"
	args+=(--hermes-home "$home")
fi

if [ -z "$key" ]; then
	if [ -t 0 ]; then
		read -rsp "Clé de la passerelle (gw-…) : " key
		echo
	fi
	[ -n "$key" ] || { echo "clé manquante : --key, ou LLMGW_API_KEY" >&2; exit 2; }
fi

# Le Python de Hermes lui-même (aucune dépendance en plus), sinon python3.
py=$("$hermes" --print-runtime-command 2>/dev/null | sed -n 's/^\["\([^"]*\)".*/\1/p' | head -n 1 || true)
[ -n "$py" ] && [ -x "$py" ] || py=$(command -v python3 || true)
[ -n "$py" ] || { echo "python3 introuvable (ni celui de Hermes, ni celui du système)" >&2; exit 1; }

# La clé passe par l'environnement du seul assistant Python, jamais par sa ligne de commande.
LLMGW_SETUP_KEY="$key" exec "$py" -I "$kit/lib/hermes_setup.py" install --url "$url" --hermes "$hermes" ${args[@]+"${args[@]}"}
