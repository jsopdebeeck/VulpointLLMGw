#!/bin/zsh
# LLMGW by Vulpoint.com : démarrage en double-clic (Terminal). Pour une icône dans la barre
# des menus, utiliser plutôt LLMGW.app (macapp/build.sh).
cd "${0:A:h}"
[ -x ./llmgw ] || go build -o llmgw . || { echo "llmgw introuvable et compilation impossible"; read; exit 1; }
listen=$(sed -n 's/^[[:space:]]*listen[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' config.toml | head -1)
port=${listen##*:}
# une passerelle déjà lancée sur ce port est arrêtée (seulement si c'est bien llmgw)
for pid in $(lsof -tiTCP:${port:-8080} -sTCP:LISTEN 2>/dev/null); do
	ps -o comm= -p $pid | grep -q llmgw && kill $pid && sleep 1
done
set -a; [ -f secrets.env ] && . ./secrets.env; set +a
(sleep 2; open "http://${listen/0.0.0.0/127.0.0.1}/dashboard") &
exec ./llmgw -config config.toml
