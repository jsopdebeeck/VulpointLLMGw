#!/usr/bin/env bash
# LLMGW by Vulpoint.com : tests du kit Hermes (plugin, install.sh, uninstall.sh) avec le Python de Hermes.
#
#   integrations/hermes/tests/run_tests.sh [--plugin-only] [-v]
#
# Tout se passe dans des dossiers Hermes TEMPORAIRES, jamais dans ~/.hermes ; une fausse passerelle
# locale répond à /v1/models, /v1/me et /v1/events. Aucun agent n'est lancé, aucun fournisseur appelé.
# Hermes prépare son environnement une fois par dossier : la première fois, le dossier « graine »
# (LLMGW_TEST_SEED, défaut $TMPDIR/llmgw-hermes-test-seed) prend quelques minutes et ~1 Go, puis
# chaque dossier de test le réutilise par lien symbolique. LLMGW_TEST_TMP : où créer les dossiers de
# test (défaut $TMPDIR) ; LLMGW_TEST_KEEP=1 les garde pour inspection.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)

pattern="test_*.py" verbose=""
while [ $# -gt 0 ]; do
	case "$1" in
	--plugin-only) pattern="test_plugin.py"; shift ;;
	-v) verbose="-v"; shift ;;
	-h | --help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
	*) echo "option inconnue : $1 (--help)" >&2; exit 2 ;;
	esac
done

hermes=${HERMES_BIN:-$(command -v hermes || true)}
[ -n "$hermes" ] || { echo "hermes introuvable (HERMES_BIN=/chemin/hermes)" >&2; exit 1; }
tmp=${LLMGW_TEST_TMP:-${TMPDIR:-/tmp}}
tmp=${tmp%/}
mkdir -p "$tmp"
tmp=$(cd "$tmp" && pwd -P)
seed=${LLMGW_TEST_SEED:-$tmp/llmgw-hermes-test-seed}
mkdir -p "$seed"
seed=$(cd "$seed" && pwd -P)
real=$(cd "$HOME" && pwd -P)/.hermes
case "$seed/" in "$real"/*) echo "LLMGW_TEST_SEED ne peut pas être dans $real" >&2; exit 1 ;; esac
case "$tmp/" in "$real"/*) echo "LLMGW_TEST_TMP ne peut pas être dans $real" >&2; exit 1 ;; esac

if [ ! -d "$seed/installs" ]; then
	echo "Préparation de l'environnement Hermes de test dans $seed (première fois : quelques minutes)…"
	HERMES_HOME="$seed" "$hermes" config path >/dev/null
fi

run=$(mktemp -d "$tmp/llmgw-hermes-run.XXXXXX")
ln -s "$seed/installs" "$run/installs"
cleanup() { case "$run" in "$tmp"/llmgw-hermes-run.*) rm -rf "$run" ;; esac; }
trap cleanup EXIT

cd "$here"
HERMES_HOME="$run" LLMGW_TEST_SEED="$seed" LLMGW_TEST_TMP="$tmp" \
	"$hermes" --run-module unittest discover -s "$here" -t "$here" -p "$pattern" $verbose
