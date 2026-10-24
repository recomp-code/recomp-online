# Recomp Online

Servidor para jugar en línea a los ports recompilados de PS2 (por ahora **DBZ Budokai Tenkaichi 4**; después
**Bloody Roar 4**): cuentas con contraseña, quién está conectado y quién busca partida, invitaciones, salas con
contraseña de 2 a 4 jugadores (por turnos) con chat, amigos, y el relay UDP por el que van las partidas.

El API va integrado en el launcher de cada juego: el jugador entra con su cuenta y desde ahí invita o se une a una
sala; cuando la partida empieza, el launcher abre el juego ya conectado al rival a través del relay.

```
launcher ──HTTPS (REST + WebSocket /api/ws)──► Caddy :443 ──► recomp-online 127.0.0.1:8090
juego    ──UDP──────────────────────────────────────────────► recomp-online :7777 (relay) ◄── juego del rival
```

- Un solo binario en Go (biblioteca estándar + `github.com/coder/websocket`). Datos en dos JSON
  (`users.json`, `sessions.json`) escritos de forma atómica.
- El relay solo reenvía entre los dos jugadores de cada partida: nadie necesita abrir puertos en su router.

## Instalar en tu servidor Linux

Necesitas un dominio (o subdominio) con un registro **A** apuntando a la IP pública del servidor, y los puertos
**443/tcp** y **7777/udp** abiertos en el firewall del proveedor si tiene uno.

```sh
git clone https://github.com/recomp-code/recomp-online.git
cd recomp-online/deploy
sudo sh install.sh online.tudominio.com
```

El script:

1. Comprueba que 443/tcp y 7777/udp estén libres (en la misma máquina puede estar el servidor de NBA Street, que usa
   el 80: no se toca).
2. Instala el programa en `/opt/recomp-online` (el de la última release, comprobando su SHA-256).
3. Crea el servicio `recomp-online` de systemd (usuario sin privilegios, datos en `/var/lib/recomp-online`).
4. Instala Caddy, que saca y renueva solo el certificado HTTPS de Let's Encrypt por el 443.
5. Abre los puertos en `ufw` si está activo y comprueba que responde.

Volver a ejecutarlo actualiza el programa y conserva las cuentas.

```sh
curl https://online.tudominio.com/api/health     # desde fuera
journalctl -u recomp-online -f                   # logs
```

Haz copia de seguridad de `/var/lib/recomp-online/users.json` (cuentas y amigos).

**Si ya tienes nginx o apache en el 443**, ejecuta `sudo SIN_CADDY=1 sh install.sh online.tudominio.com` y añade un
proxy a `127.0.0.1:8090` en tu servidor web, con WebSocket permitido en `/api/ws`.

## Seguridad

- Contraseñas con PBKDF2-SHA256 (210 000 iteraciones, sal por cuenta). Las sesiones se guardan como hash SHA-256:
  quien lea el disco no puede usarlas.
- El token de sesión solo se acepta en la cabecera `Authorization` (nunca en la URL). En el PC del jugador se
  guarda cifrado con DPAPI de Windows.
- Límite de intentos por IP en registro y login, límite de conexiones por IP y en total, cabeceras de seguridad.
  La IP real se toma de `X-Forwarded-For` solo cuando la conexión viene del proxy local.
- Salas con contraseña (hash con sal) y las partidas con un token aleatorio de 128 bits. El relay solo acepta a los
  dos jugadores registrados con ese token, no deja que otro se quede con la dirección de un jugador que sigue
  enviando, y limita los paquetes por segundo.
- Reconexión: si el launcher pierde la conexión reintenta al momento, y el servidor le guarda el sitio (estado,
  sala, invitaciones) 15 segundos.

## Desarrollo

```sh
cd server
go test ./...
go run . --http 127.0.0.1:8090 --relay 127.0.0.1:7777 --relay-public 127.0.0.1:7777 --data ./data
```

`server/tools/bot` es un jugador de prueba (entra, busca partida, invita, crea salas, habla en el chat):

```sh
go run ./tools/bot -user Bulma -pass capsule1 -waiting -invite Miguel
```

Binarios para la release: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"` (y `arm64`),
más su `SHA256SUMS`.

### API

| Método | Ruta | |
|---|---|---|
| GET | `/api/health` | estado |
| POST | `/api/register`, `/api/login` | `{user, password}` → `{token, user}` |
| POST | `/api/logout`, `/api/password` | cerrar sesión, cambiar contraseña (cierra las demás sesiones) |
| GET | `/api/ws?game=&build=` | WebSocket del lobby en vivo (`hello`, `lobby`, `room`, `invite`, `match`, `chat`...) |
| POST | `/api/status` | `{waiting}` buscar partida |
| POST | `/api/invite`, `/api/invite/respond` | invitar, aceptar o rechazar |
| POST | `/api/rooms`, `/api/rooms/join`, `/leave`, `/start`, `/kick` | salas |
| POST | `/api/rooms/mute` | `{user, mute}` el creador silencia a alguien para toda la sala (chat de voz) |
| POST | `/api/chat` | mensaje a la sala |
| POST | `/api/match/end` | fin de la partida (la sala pasa al siguiente turno) |
| POST | `/api/friends` | `{user, add}` |

Todas las rutas salvo `health`, `register` y `login` piden `Authorization: Bearer <token>`.

### Chat de voz

La voz va por el mismo puerto UDP del relay, desde un socket del launcher (nunca el del juego). Un canal es una
**sala** (hasta 4: los dos que pelean y los que esperan turno siguen hablando entre peleas y con la rotación) o la
partida 1 contra 1 de una invitación (se cierra con la partida).

- Al entrar en la sala (o empezar la partida de una invitación) el WebSocket manda a cada jugador, solo a él,
  `{"type":"voice","channel","relay","slot":1-4,"token"}`; al volver a conectarse recibe el mismo sitio y token.
  `room.voice` (`{usuario: slot}`) y `room.muted` dicen quién es quién; en la partida de una invitación, `match.voice`.
  Al salir llega `voice_end`.
- UDP: `"RLV1" + token` registra (respuesta `"RLVA" + slot`; se repite cada pocos segundos para mantener abierto el
  NAT en silencio). `'v' + slot + datos`: voz; el relay escribe el slot de quien la manda (no se fía del que traiga)
  y la copia a los demás del canal sin decodificarla. `'p' + datos`: ping, vuelve igual a quien lo manda.
- Quién oye a quién se calcula cuando cambia (entradas, salidas, bloqueos, silencio del creador): entre dos jugadores
  con un bloqueo de por medio no pasa la voz en ningún sentido. Máximo 250 datagramas por segundo por jugador.
- Lo que añade el relay (`go test -run VoiceRelayLatency -v`, 4 hablando a la vez a 100 datagramas/s): p50 ~0.26 ms,
  p99 ~0.6 ms en local; ~10 µs de CPU por datagrama recibido con sus 3 envíos, sin reservas de memoria.

En el launcher (`launcher/voice/`) la voz es un motor en C, para que el audio nunca espere al recolector de Go: 48 kHz
mono en tramos de 10 ms, Opus (VOIP, con FEC dentro del paquete), un buffer de jitter por jugador que se adapta solo
(20 ms en una red limpia, 80 ms como mucho), relleno de huecos, control de la deriva de reloj entre tarjetas de sonido y
una puerta de ruido por mínimos estadísticos (un ventilador constante no la abre; la voz por encima sí). Silencio = no
se manda nada. En macOS va compilado dentro del launcher (cgo); en Windows es una DLL metida en `Recomp.exe`
(`recompvoice_windows_amd64.dll`, se carga sin cgo). `launcher/voice/build.sh` rehace Opus y la DLL desde un Mac (zig).

- Micrófono: se abre al hablar o mientras se mantiene Alt derecho / Opción derecha. Silenciado, el micrófono ni se
  abre. Durante la partida Shift+M lo cierra o lo abre (con un tono). Cada jugador: volumen y silencio propios.
- macOS: cancelación de eco opcional (la unidad de voz de Apple, para quien juega con las bocinas).
- Pruebas: `launcher/voice/test/e2e.c` (4 motores por un relay con pérdidas y jitter: latencia, huecos, mezcla,
  silencios, puerta, deriva), `test/devices.c` (los altavoces de verdad) y `RECOMP_VOICE_E2E=1 go test -run
  VoiceServer` en `launcher/` (el launcher contra este servidor).

## Licencia

GPL-3.0, como los ports.
