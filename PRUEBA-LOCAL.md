# Guía de prueba local — Omnia Cloud multi-cuenta

Probar en tu máquina, antes del homelab: **subida de memorias** + **aislamiento entre cuentas/clouds** + **RBAC**.

Simulamos el escenario real: un Omnia local enlazado a DOS clouds a la vez (trabajo + personal),
cada uno con su cuenta y sus proyectos, sin que se mezclen.

---

## ⚠️ Lo único que tenés que recordar (gotcha)

Tu shell tiene `ENGRAM_CLOUD_TOKEN` exportado globalmente (apunta a tu cloud real de producción).
Ese env var **pisa** el token de `cloud.json` por diseño (sirve para CI/overrides).

Para TODAS las pruebas locales usá `env -u ENGRAM_CLOUD_TOKEN` para que el cliente use el token
de la cuenta de prueba y no el de producción. Sin esto vas a ver `401: invalid bearer token`.

Y siempre con un `ENGRAM_DATA_DIR` aislado: **nunca toques `~/.engram`** (es tu memoria real).

---

## 0. Estado actual (ya está levantado)

Ahora mismo en tu máquina:

| Cloud     | URL                     | Cuenta          | Password          | Proyecto      | DB Postgres            |
|-----------|-------------------------|-----------------|-------------------|---------------|------------------------|
| trabajo   | http://127.0.0.1:18090  | `benja-work`    | `workpass123`     | `proj-trabajo`| `engram_cloud_work`    |
| personal  | http://127.0.0.1:18091  | `benja-personal`| `personalpass123` | `proj-empresa`| `engram_cloud_personal`|

Binario de prueba: `/tmp/omnia-mc-engram`  ·  Logs: `/tmp/omnia-mc/<alias>.log`

Verificar que ambos respondan:

```bash
for p in 18090 18091; do printf "port %s: " "$p"; curl -s -o /dev/null -w "%{http_code}\n" "http://127.0.0.1:$p/health"; done
# Esperado: 200 y 200
```

Si están caídos, levantalos: `cd ~/code/omnia-core && scripts/dev-multicloud-up.sh`

---

## 1. Probar la SUBIDA de memorias — máquina "trabajo"

```bash
BIN=/tmp/omnia-mc-engram
DD=/tmp/omnia-local-work          # data dir aislado = "esta máquina"
run(){ env -u ENGRAM_CLOUD_TOKEN ENGRAM_DATA_DIR="$DD" "$BIN" "$@"; }

# 1) crear memorias locales en el proyecto de trabajo
run save "Pricing v2 de Velion" "Nuevo esquema de precios por seats" --project proj-trabajo
run save "Bug callback Flow"     "El webhook reintenta 3 veces y duplica"  --project proj-trabajo

# 2) apuntar al cloud de trabajo y loguear con la cuenta
run cloud config --server http://127.0.0.1:18090
run cloud login --username benja-work --password workpass123

# 3) habilitar el proyecto y SUBIR
run cloud enroll proj-trabajo
run sync --cloud --project proj-trabajo
# Esperado: "Cloud sync complete for project proj-trabajo"
```

Verificar que llegaron al cloud:

```bash
psql -tA engram_cloud_work -c \
  "SELECT payload->>'title' FROM cloud_mutations WHERE project='proj-trabajo' AND entity='observation';"
# Esperado: Pricing v2 de Velion / Bug callback Flow
```

---

## 2. Probar la SUBIDA — segunda máquina/cuenta "personal"

Otro data dir = otra máquina, otra cuenta, otro cloud:

```bash
BIN=/tmp/omnia-mc-engram
DD2=/tmp/omnia-local-personal
runp(){ env -u ENGRAM_CLOUD_TOKEN ENGRAM_DATA_DIR="$DD2" "$BIN" "$@"; }

runp save "Idea producto propio" "Arquitectura del SaaS de mi empresa" --project proj-empresa
runp save "Roadmap Q3"           "Hitos del trimestre"                  --project proj-empresa

runp cloud config --server http://127.0.0.1:18091
runp cloud login --username benja-personal --password personalpass123
runp cloud enroll proj-empresa
runp sync --cloud --project proj-empresa
```

---

## 3. Verificar el AISLAMIENTO entre clouds (lo importante)

```bash
echo -n "personal tiene proj-empresa (=2): " && psql -tA engram_cloud_personal -c "SELECT count(*) FROM cloud_mutations WHERE project='proj-empresa' AND entity='observation';"
echo -n "work    tiene proj-empresa (=0): " && psql -tA engram_cloud_work     -c "SELECT count(*) FROM cloud_mutations WHERE project='proj-empresa';"
echo -n "work    tiene proj-trabajo (=2): " && psql -tA engram_cloud_work     -c "SELECT count(*) FROM cloud_mutations WHERE project='proj-trabajo' AND entity='observation';"
echo -n "personal tiene proj-trabajo (=0): " && psql -tA engram_cloud_personal -c "SELECT count(*) FROM cloud_mutations WHERE project='proj-trabajo';"
```

Cada memoria queda SOLO en su cloud. Cero cruce.

---

## 4. Verificar el RBAC (cuenta ajena no puede leer/escribir)

Una cuenta sin membresía en un proyecto debe recibir 403. El test E2E ya cubre esto:

```bash
cd ~/code/omnia-core && env -u ENGRAM_CLOUD_TOKEN scripts/e2e-cloud-isolation.sh
# Esperado: 7/7 OK (alice -> proyecto de bob = 403; con grant dinámico = 200)
```

---

## 5. (Opcional) Ver las memorias en el dashboard

El dashboard vive en el repo principal (no en el fork del cloud) y sirve para mirar las
memorias de un data dir aislado (no toca producción):

```bash
cd ~/Documents/01.-\ Velion/omnia
ENGRAM_DATA_DIR=/tmp/omnia-local-work go run ./cmd/omnia dashboard --port 7800
# Abrí http://127.0.0.1:7800
```

---

## 5b. Dashboard DEL CLOUD (consola Omnia + RBAC por cuenta)

Cada cloud trae su consola web en `/dashboard`, con el diseño command-center de Omnia
(no toca producción). Muestra lo que vive en su Postgres, filtrado por tu cuenta:

- Work:     **http://127.0.0.1:18090/dashboard**
- Personal: **http://127.0.0.1:18091/dashboard**

**Login con tu cuenta** (botón *Sign In*, usuario/contraseña) — ves SOLO tus proyectos:

| Usuario | Contraseña | Ve |
|---|---|---|
| `benja-work` | `workpass123` | solo `proj-trabajo` + sus memorias |
| `otra-empresa` | `otrapass123` | solo `proj-otra` (para probar el aislamiento entre cuentas) |
| `benja-personal` | `personalpass123` | en el cloud personal (:18091): `proj-empresa` |

**Login como operador del server** (botón *Sign in as server operator*, con el admin token) —
ve TODO + el panel Admin:

- work → `mc-work-dashboard-admin`
- personal → `mc-personal-dashboard-admin`

> El token de operador del dashboard sale de `ENGRAM_CLOUD_ADMIN`, **distinto** del
> `ENGRAM_CLOUD_TOKEN` de sync (que NO da acceso admin — separación de privilegios).

Qué comprobar:
- `benja-work` ve solo `proj-trabajo`; si tipeás la URL de un proyecto ajeno → **404**, y no ve el panel Admin.
- `otra-empresa` no ve `proj-trabajo` ni sus memorias por ningún lado (lista, browser, URL directa).
- El operador ve todos los proyectos + el panel Admin.

> El dashboard solo carga proyectos dentro de `ENGRAM_CLOUD_ALLOWED_PROJECTS` (el script lo deja
> en `"*"`); sobre esa base, el RBAC por cuenta hace el filtro real.

---

## 6. Parar / limpiar

```bash
cd ~/code/omnia-core && scripts/dev-multicloud-down.sh     # baja los dos clouds
rm -rf /tmp/omnia-local-work /tmp/omnia-local-personal     # borra los data dirs de prueba
```

Tu memoria real (`~/.engram`) y tu cloud de producción **no se tocan** en ningún paso.

---

## Cuando lo lleves al homelab

- Reemplazá `http://127.0.0.1:PORT` por la URL del homelab (con TLS).
- Cada cloud = su propia DB Postgres + `ENGRAM_JWT_SECRET` fuerte + `ENGRAM_CLOUD_TOKEN` admin propio.
- Ver `OMNIA-CLOUD.md` y `docker-compose.cloud.yml` para el despliegue con contenedores.
- El flujo del cliente es idéntico: `cloud config` → `cloud login` → `cloud enroll` → `sync --cloud`.
