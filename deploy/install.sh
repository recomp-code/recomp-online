#!/bin/sh
# Instala (o actualiza) recomp-online en un Linux con systemd.
#   sudo sh install.sh online.tudominio.com
# Antes: el dominio tiene que apuntar (registro A) a la IP pública de este servidor.
# Pone el servidor en /opt/recomp-online, el servicio en systemd, Caddy para el HTTPS y abre 443/tcp y 7777/udp
# en ufw si está activo. Volver a ejecutarlo actualiza el programa y conserva las cuentas.
set -eu

DOMAIN="${1:-}"
if [ -z "$DOMAIN" ]; then
    echo "uso: sudo sh install.sh online.tudominio.com" >&2
    exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
    echo "ejecútalo con sudo" >&2
    exit 1
fi
HERE="$(cd "$(dirname "$0")" && pwd)"
RELEASES="https://github.com/recomp-code/recomp-online/releases/latest/download"

case "$(uname -m)" in
    x86_64|amd64)  NAME="recomp-online-linux-amd64" ;;
    aarch64|arm64) NAME="recomp-online-linux-arm64" ;;
    *) echo "arquitectura $(uname -m) no soportada" >&2; exit 1 ;;
esac

# En esta máquina ya corre el servidor de NBA Street: antes de tocar nada, que 443/tcp y 7777/udp estén libres
# (o los tenga ya Caddy / recomp-online, si es una actualización).
busy() { # $1 = t|u, $2 = puerto: imprime quién lo usa, si no es nuestro
    ss -H -ln${1}p "sport = :$2" 2>/dev/null | grep -v -e '"caddy"' -e '"recomp-online"' | head -n1
}
if command -v ss >/dev/null 2>&1; then
    if [ -n "$(busy t 443)" ]; then
        echo "El puerto 443/tcp ya lo usa otro programa:" >&2; busy t 443 >&2
        echo "Si es un nginx/apache con tus webs, pon recomp-online detrás de él (proxy a 127.0.0.1:8090, con WebSocket en /api/ws)" >&2
        echo "y vuelve a ejecutar con SIN_CADDY=1." >&2
        [ "${SIN_CADDY:-}" = "1" ] || exit 1
    fi
    if [ -n "$(busy u 7777)" ]; then
        echo "El puerto 7777/udp ya lo usa otro programa:" >&2; busy u 7777 >&2; exit 1
    fi
fi

echo "== 1/4 programa"
BIN="$HERE/$NAME"
if [ ! -f "$BIN" ]; then
    echo "descargando $NAME de la última versión publicada"
    BIN="$(mktemp)"
    curl -fsSL "$RELEASES/$NAME" -o "$BIN"
    curl -fsSL "$RELEASES/SHA256SUMS" | grep " $NAME\$" | sed "s|$NAME\$|$BIN|" | sha256sum -c -
fi
install -d /opt/recomp-online
install -m 755 "$BIN" /opt/recomp-online/recomp-online

echo "== 2/4 servicio"
sed "s/TU_DOMINIO/$DOMAIN/g" "$HERE/recomp-online.service" > /etc/systemd/system/recomp-online.service
systemctl daemon-reload
systemctl enable recomp-online >/dev/null
systemctl restart recomp-online

echo "== 3/4 HTTPS (Caddy)"
if [ "${SIN_CADDY:-}" = "1" ]; then
    echo "(SIN_CADDY=1: el HTTPS lo pone tu propio servidor web)"
elif ! command -v caddy >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
        # repositorio oficial de Caddy (https://caddyserver.com/docs/install#debian-ubuntu-raspbian)
        apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
        apt-get update
        apt-get install -y caddy
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y 'dnf-command(copr)' && dnf copr enable -y @caddy/caddy && dnf install -y caddy
    else
        echo "instala Caddy a mano (https://caddyserver.com/docs/install) y vuelve a ejecutar este script" >&2
        exit 1
    fi
fi
if [ "${SIN_CADDY:-}" = "1" ]; then
    :
elif [ -f /etc/caddy/Caddyfile ] && ! grep -q 'recomp-online' /etc/caddy/Caddyfile && ! grep -q 'root \* /usr/share/caddy' /etc/caddy/Caddyfile; then
    # ya hay un Caddyfile propio: no se toca, se deja el bloque para añadirlo a mano
    sed "s/TU_DOMINIO/$DOMAIN/g" "$HERE/Caddyfile" > /etc/caddy/recomp-online.caddy
    echo "AVISO: ya tienes un /etc/caddy/Caddyfile propio. Añade el bloque de /etc/caddy/recomp-online.caddy y recarga Caddy."
else
    if [ -f /etc/caddy/Caddyfile ]; then cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.antes-recomp-online"; fi
    { echo "# recomp-online"; sed "s/TU_DOMINIO/$DOMAIN/g" "$HERE/Caddyfile"; } > /etc/caddy/Caddyfile
    systemctl enable caddy >/dev/null
    systemctl restart caddy
fi

echo "== 4/4 firewall"
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q 'Status: active'; then
    ufw allow 443/tcp
    ufw allow 7777/udp
else
    echo "(ufw no está activo; si el proveedor tiene firewall propio, abre 443/tcp y 7777/udp ahí)"
fi

sleep 2
if curl -fsS http://127.0.0.1:8090/api/health >/dev/null; then
    echo "OK: recomp-online está funcionando."
else
    echo "El servicio no responde. Mira: journalctl -u recomp-online -n 50" >&2
    exit 1
fi
echo "Prueba desde fuera (puede tardar 1 minuto la primera vez, mientras se emite el certificado):"
echo "    curl https://$DOMAIN/api/health"
echo "Logs: journalctl -u recomp-online -f      Cuentas: /var/lib/recomp-online/users.json (haz copia de seguridad)"
