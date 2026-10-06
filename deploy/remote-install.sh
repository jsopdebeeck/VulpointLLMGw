#!/usr/bin/env bash
# llmgw : partie serveur de deploy/deploy.sh, exécutée en root.
#   remote-install.sh <dossier de transit> <nom du binaire> [--caddy]
# Valide la configuration avec le nouveau binaire avant de remplacer l'ancien,
# et revient à l'ancien binaire si le service ne redémarre pas.
set -euo pipefail

stage="$1" bin="$2" caddy="${3:-}"
dir=/opt/llmgw
unit=/etc/systemd/system/llmgw.service
trap 'rm -rf "$stage"' EXIT

id llmgw >/dev/null 2>&1 || useradd --system --home-dir "$dir" --shell /usr/sbin/nologin llmgw
install -d -o llmgw -g llmgw -m 700 "$dir"

if [ -f "$stage/config.toml" ]; then
	[ -f "$dir/config.toml" ] && cp -p "$dir/config.toml" "$dir/config.toml.bak-deploy-$(date +%Y%m%d-%H%M%S)"
	install -o llmgw -g llmgw -m 600 "$stage/config.toml" "$dir/config.toml"
	echo "✓ config.toml remplacé"
elif [ ! -f "$dir/config.toml" ]; then
	echo "✗ pas de $dir/config.toml : relancer deploy.sh avec --config config.toml" >&2
	exit 1
fi

# lu par systemd (root) avant de lancer le service : reste en root:root 600
if [ ! -f "$dir/secrets.env" ]; then
	install -o root -g root -m 600 /dev/null "$dir/secrets.env"
	echo "! $dir/secrets.env créé vide : y mettre MISTRAL_API_KEY=… etc., puis systemctl restart llmgw"
fi

install -o llmgw -g llmgw -m 755 "$stage/$bin" "$dir/llmgw.new"
if ! runuser -u llmgw -- "$dir/llmgw.new" -config "$dir/config.toml" -check; then
	rm -f "$dir/llmgw.new"
	echo "✗ configuration refusée par le nouveau binaire, rien n'a été changé" >&2
	exit 1
fi
[ -f "$dir/llmgw" ] && mv "$dir/llmgw" "$dir/llmgw.prev"
mv "$dir/llmgw.new" "$dir/llmgw"

install -m 644 "$stage/llmgw.service" "$unit"
systemctl daemon-reload
systemctl enable --quiet llmgw
systemctl restart llmgw
sleep 3
if ! systemctl is-active --quiet llmgw; then
	journalctl -u llmgw -n 30 --no-pager >&2
	if [ -f "$dir/llmgw.prev" ]; then
		mv "$dir/llmgw.prev" "$dir/llmgw"
		systemctl restart llmgw
		echo "✗ le nouveau binaire ne démarre pas : retour à la version précédente" >&2
	else
		echo "✗ llmgw ne démarre pas (journal ci-dessus)" >&2
	fi
	exit 1
fi
echo "✓ llmgw actif ($(systemctl show -p MainPID --value llmgw))"

listen=$(sed -n 's/^listen *= *"\([^"]*\)".*/\1/p' "$dir/config.toml" | head -1)
case "$listen" in
"" | 127.0.0.1:8080) ;;
*) echo "! listen = \"$listen\" : le Caddyfile relaie vers 127.0.0.1:8080, à aligner" ;;
esac
grep -qE '^admin_token *= *"(change-moi)?"' "$dir/config.toml" &&
	echo "! admin_token vide ou par défaut : le changer avant d'exposer le serveur"

if [ "$caddy" = --caddy ]; then
	if ! command -v caddy >/dev/null; then
		echo "→ installation de Caddy"
		apt-get update -qq && apt-get install -y -qq caddy
	fi
	install -d -o caddy -g caddy /var/log/caddy
	caddy validate --adapter caddyfile --config "$stage/Caddyfile" >/dev/null
	if [ -f /etc/caddy/Caddyfile ] && ! grep -q 'llmgw derrière Caddy' /etc/caddy/Caddyfile; then
		cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak-$(date +%Y%m%d-%H%M%S)"
	fi
	install -m 644 "$stage/Caddyfile" /etc/caddy/Caddyfile
	systemctl enable --quiet caddy
	systemctl reload-or-restart caddy
	echo "✓ Caddy configuré ($(sed -n '/^[a-z0-9.-]* {$/{s/ {//p;q}' /etc/caddy/Caddyfile))"
fi

command -v ufw >/dev/null && ufw status | grep -q inactive &&
	echo "! pare-feu ufw inactif : ufw allow OpenSSH && ufw allow 80,443/tcp && ufw enable"
exit 0
