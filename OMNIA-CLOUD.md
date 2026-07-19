# Omnia Cloud — multi-tenant (guía de deploy y uso)

Servidor de memoria compartida con **cuentas, permisos por proyecto y aislamiento
entre cuentas**. Cada Omnia local sincroniza al cloud autenticándose como su cuenta;
una cuenta solo ve los proyectos donde tiene permiso.

Estado: probado end-to-end localmente (`scripts/e2e-cloud-isolation.sh`, 7/7).

## Qué hay implementado

- **Cuentas** (`/auth/signup`, `/auth/login`) — bcrypt + tokens de cuenta (HMAC, TTL 24h).
- **Permisos por proyecto** (CRUD: Read/Insert/Update/Delete) — tabla `cloud_memberships`.
- **Aislamiento**: cada operación (lectura/escritura) chequea el membership de la cuenta;
  una cuenta nunca recibe datos de proyectos donde no es miembro.
- **Roles** owner > admin > moderator > member, con endpoints de gestión de miembros
  (`GET/POST /projects/{project}/members`, `DELETE .../{account_id}`) y prevención de
  escalación de privilegios.
- **Claim al primer push**: la primera cuenta que sincroniza un proyecto nuevo queda como
  su `owner`.
- Compatibilidad hacia atrás con el token único legacy (allowlist) intacta.

- **Scope por dispositivo** (Fase 4) — cada device puede restringirse a ciertos proyectos;
  se administra con `omnia cloud devices list|scope|revoke` (ver 3.1). `last_seen_at` registra
  actividad por device.
- **Bootstrap del primer admin** (`omnia cloud bootstrap-admin`) y signup cerrado por defecto.

Pendiente (no bloquea probar): namespacing por cuenta para nombres de proyecto repetidos
(Fase 5), refresh token (hoy hay que re-loguear cada 24h).

## 1. Levantar el cloud server en el homelab (Docker)

Usá el `docker-compose.cloud.yml` del repo, pero en **modo auth** (no inseguro). Variables
clave del servicio `cloud`:

```yaml
environment:
  ENGRAM_DATABASE_URL: postgres://engram:UNA_PASS_FUERTE@postgres:5432/engram_cloud?sslmode=disable
  ENGRAM_JWT_SECRET: <secreto de 32+ bytes, NO el de ejemplo>   # firma los tokens de cuenta
  ENGRAM_CLOUD_TOKEN: <token admin/legacy>                       # habilita modo auth
  ENGRAM_CLOUD_ALLOWED_PROJECTS: _legacy_unused                  # requerido al iniciar; con cuentas mandan los memberships
  ENGRAM_CLOUD_HOST: 0.0.0.0
  ENGRAM_PORT: "18080"
# y quitá ENGRAM_CLOUD_INSECURE_NO_AUTH
```

```bash
docker compose -f docker-compose.cloud.yml up -d --build
```

El esquema (cuentas, memberships, etc.) se migra solo al arrancar.

> **Seguridad**: poné el server detrás de HTTPS (reverse proxy: Caddy/Traefik/nginx). Sin TLS,
> contraseñas y tokens viajan en claro. Generá `ENGRAM_JWT_SECRET` con `openssl rand -base64 48`.

## 2. Crear cuentas y dar acceso

> **Signup cerrado por defecto (OBL-02)**: en un server accesible por LAN, `POST /auth/signup`
> ya **no** está abierto. Se provisiona el **primer admin** con la CLI (contra el storage del
> propio server, sin HTTP) y de ahí en más se administran cuentas con el admin API. Si querés
> re-abrir el self-signup deliberadamente (p. ej. para seeding en dev), poné
> `ENGRAM_CLOUD_OPEN_SIGNUP=1` en el env del server.

**Primer admin (en el host del server):**

```bash
# crea el primer admin contra el storage del server (mismo ENGRAM_DATABASE_URL).
# Es idempotente: si ya existe alguna cuenta, se niega.
omnia cloud bootstrap-admin --username benja --password '...'

# opcional: además emite un managed token (se muestra UNA sola vez).
# Requiere ENGRAM_CLOUD_MANAGED_TOKENS=1 y un ENGRAM_CLOUD_TOKEN_PEPPER no-default.
omnia cloud bootstrap-admin --username benja --password '...' --issue-token
```

**Cuentas adicionales**: con signup cerrado, usá el admin API (operador, `ENGRAM_CLOUD_ADMIN`)
o re-abrí signup temporalmente con `ENGRAM_CLOUD_OPEN_SIGNUP=1`. Con signup abierto:

```bash
S=https://engram.tu-homelab        # o http://IP:18080 en LAN

curl -X POST $S/auth/signup -H 'Content-Type: application/json' \
  -d '{"username":"otro","email":"otro@...","password":"..."}'
```

> Reintentar un signup con un username ya tomado ahora **falla limpio** (409) y **no** pisa el
> email de la cuenta existente (antes lo sobrescribía en silencio).

Cada cuenta se vuelve **owner** de un proyecto la primera vez que lo sincroniza (push).
Para compartir un proyecto, el owner/admin agrega miembros:

```bash
# login para obtener tu token de cuenta
TOK=$(curl -s -X POST $S/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"benja","password":"..."}' | jq -r .token)

# dar READ del proyecto "velion" a la cuenta id=2
curl -X POST $S/projects/velion/members -H "Authorization: Bearer $TOK" \
  -H 'Content-Type: application/json' -d '{"account_id":"2","perms":1,"role":"member"}'
```

Permisos (bitmask): Read=1, Insert=2, Update=4, Delete=8 (suma para combinar; 15 = todo).

## 3. Conectar cada Omnia local

En cada máquina con engram/omnia local:

```bash
omnia cloud config --server https://engram.tu-homelab
omnia cloud login --username benja           # pide la contraseña; guarda el token en cloud.json
```

A partir de ahí, cada sync se autentica como esa cuenta. Re-logueá cuando el token expire
(24h) hasta que sumemos refresh.

### 3.1. Scope por dispositivo (test de dos notebooks)

Cada notebook se registra como un **device** al loguear con `--device`, y podés restringirlo a
ciertos proyectos. El scope es **aditivo**: un device con scope vacío es *irrestricto* (ve todo
lo que su cuenta puede ver); con scope no-vacío, solo esos proyectos (nunca otorga más de lo
que el membership permite).

```bash
# en cada notebook, logueá ligando el token a un device con nombre
omnia cloud login --username benja --device notebook-a
omnia cloud login --username benja --device notebook-b   # en la otra máquina

# administrar devices (usa el token de cuenta guardado en cloud.json)
omnia cloud devices list                                  # nombre, id, scope, last_seen
omnia cloud devices scope notebook-a --projects proj-trabajo
omnia cloud devices scope notebook-b --projects proj-empresa
omnia cloud devices scope notebook-a --projects ''        # '' = irrestricto (limpia el scope)
omnia cloud devices revoke notebook-b
```

- `last_seen` refleja el último request autenticado de ese device (se actualiza en cada sync).
- **Revocar** un device niega su scope **de inmediato** (fail-closed): a partir de ese momento
  ninguna operación de proyecto con ese device pasa. El **token de cuenta** ya emitido para ese
  device sigue válido hasta que expire (TTL 24h) o se refresque — el binding criptográfico
  token↔device es un hardening opcional aparte, no requerido acá.

## 4. Probar el aislamiento

```bash
scripts/e2e-cloud-isolation.sh    # requiere un Postgres local, jq, curl
```

Levanta un server efímero y verifica: cada cuenta lee lo suyo (200), no lo ajeno (403), y el
acceso se abre solo cuando el owner lo concede.
