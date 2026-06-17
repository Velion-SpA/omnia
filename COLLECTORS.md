# Omnia — Colectores de fuentes externas

Estado y próximos pasos para traer actividad externa hacia Omnia/Engram.

Omnia ya tiene un pipeline de colección reutilizable (`internal/core` con Source/Sink/StateStore,
`internal/source/*`, `internal/sink/engram`). El cron nocturno `sync` (02:00, launchd
`com.velion.omnia`) trae datos de cada fuente habilitada y los guarda en Engram como
observaciones (idempotente por `topic_key`). Este documento lleva el registro del estado de
cada fuente y de lo que tenés que decidir o hacer para encender las que faltan.

---

## GitHub — commits (NUEVO, apagado por defecto)

**Qué cambió:** el source de GitHub ya traía issues y PRs (por el endpoint `/issues`). Ahora
además trae los **commits** con sus autores — `sha`, mensaje, el usuario de GitHub *y* el
nombre/email de git, fecha y URL — como observaciones `github-commit`. Esto es exactamente lo
que pediste: "commits, quiénes los hicieron".

- Código: `internal/source/github/github.go` (`commitResp`, `fetchCommitItems`, `fetchCommits`,
  `formatCommit`). Config: `internal/config/config.go` (`GitHubConfig.IncludeCommits`,
  `MaxCommitsPerRepo`). Wiring: `cmd/omnia/main.go` `runSync`.
- Idempotente: `topic_key = github/<owner>-<repo>/commit-<sha>`, así que volver a correrlo
  nunca duplica.
- Cursor propio por repo (`<repo>#commits`) para no interferir con el cursor de issues/PRs.
- Verificado de punta a punta (dry-run, API real) contra `saluvita`; con tests unitarios.

**Por qué está APAGADO por defecto:** la primera corrida trae hasta `max_commits_per_repo`
(default **300**) por repo dentro de la ventana de `backfill_days`. Entre todos tus repos eso
pueden ser *muchísimas* observaciones nuevas en Engram de golpe. No quise inundar Engram
mientras dormías. Vos decidís cuándo prenderlo.

**Para activarlo:**

Primero, desplegá el binario nuevo (el binario de producción en `~/.local/bin/omnia` es anterior
a este código). Con `include_commits` todavía en false esto no cambia el comportamiento — el
cron sigue haciendo exactamente lo de hoy (solo issues/PRs) hasta que prendas la opción:

```bash
cp ~/.local/bin/omnia ~/.local/bin/omnia.bak-$(date +%Y%m%d-%H%M%S)   # backup
go build -o ~/.local/bin/omnia ./cmd/omnia                            # desde la raíz del repo
```

Después editá `~/.config/omnia/config.yaml`:

```yaml
sources:
  github:
    enabled: true              # ya está en true
    include_commits: true      # <-- agregá esto
    max_commits_per_repo: 100  # opcional; default 300. Más bajo = primer backfill más suave
```

Después esperá al cron de las 02:00 o corrélo a mano. **Probá primero sin escribir nada:**

```bash
omnia --dry-run --source github sync   # muestra lo que TRAERÍA, sin guardar
omnia --source github sync             # trae de verdad
```

Tip: para que el primer backfill sea chico, fijá una ventana corta una vez:
`omnia --since 2026-06-10T00:00:00Z --source github sync`.

---

## GitHub — issues / PRs (ya activo)

`enabled: true` y corriendo en el cron de las 02:00. No hay que hacer nada. Los commits viajan
por el mismo cron.

---

## Discord (implementado, deshabilitado — necesita un bot)

`internal/source/discord/discord.go` **ya está completo**: lee el historial de mensajes de los
canales por la API REST de Discord y guarda una observación `message_digest` por canal por día,
con manejo de rate-limit, paginación, cursores (snowflake) y ruteo a proyecto. Es **solo
lectura** — nunca publica nada. No hay que construir nada; solo le faltan credenciales.

**Para activarlo:**

1. Creá una app de bot en <https://discord.com/developers/applications> → New Application → Bot.
2. Copiá el **token del bot**.
3. Invitá el bot a tu servidor con los permisos mínimos: **View Channel** + **Read Message
   History** solamente (OAuth2 URL generator → scope `bot`, esos dos permisos).
4. Activá el **Message Content Intent** en la config del bot (necesario para leer el cuerpo de
   los mensajes).
5. Conseguí los IDs de los canales (Discord → Ajustes → Avanzado → Modo desarrollador →
   clic derecho en el canal → Copiar ID).
6. Configurá:

```yaml
sources:
  discord:
    enabled: true
    token: ""                       # mejor: exportá DISCORD_BOT_TOKEN en el plist de launchd
    channels:
      - { id: "123...", name: "general", guild: "mi-servidor" }
    project: omnia                   # o ruteá por canal con el mapa `routes:`
```

**Notas de cuidado (tu "con cuidado"):** mantené el bot limitado solo a los canales que querés,
con permisos de solo lectura. No subas el token a git — mejor usá la variable `DISCORD_BOT_TOKEN`
en `EnvironmentVariables` del plist. El colector solo lee; no puede enviar ni moderar.

> Nota: dijiste "bot **helper** de discord". Este source *ingiere* mensajes (solo lectura). Si
> lo que querés es un bot que *responda/ayude* en Discord, eso es otra construcción aparte —
> decime y lo planifico.

---

## WhatsApp (NO implementado — necesito que decidas) ⚠️

Pediste tu chat contigo mismo ("notas para mí"). A propósito **no** construí esto todavía,
porque las únicas formas de leer una cuenta personal de WhatsApp son riesgosas. Estas son las
opciones reales:

| Opción | Cómo | Riesgo |
|---|---|---|
| **A. Exportar a mano → importar** (recomendada) | En WhatsApp: abrí el chat contigo mismo → ⋯ → Exportar chat → compartí el `.txt`/`.zip`. Después `omnia import whatsapp <archivo>`. | **Ninguno para tu cuenta.** Manual, vos controlás cuándo. |
| B. Librería no oficial (Baileys / whatsapp-web.js) | Scrapea WhatsApp Web con una sesión vinculada (escanear un QR, mantenerla viva). | **Te pueden BANEAR el número personal.** Viola los términos de WhatsApp. Frágil (se rompe con cada update de WA). |
| C. WhatsApp Cloud / Business API (oficial) | La API oficial de Meta. | Solo para números de **empresa**, no tu cuenta personal / chat contigo mismo. No sirve. |

**Mi recomendación: Opción A.** Es segura, y el export del chat contigo mismo es justo "las
cosas que me mando a mí mismo" que describiste. Puedo implementar `omnia import whatsapp <export>`
rápido — pero el formato de texto del export varía según el idioma (es-CL) y según iOS vs
Android, así que **necesito un export de muestra tuyo para calibrar el parser**. Dejá un chat
exportado chico en el repo (o pegame unas líneas) y lo dejo andando.

Si querés la Opción B a pesar del riesgo de ban, decímelo de forma explícita y la construyo
aislada detrás de un flag deshabilitado — pero no la voy a apuntar a tu número real sin tu visto
bueno claro.

---

## Cron — no hace falta un job nuevo

El job de launchd que ya existe (`com.velion.omnia`, 02:00, `omnia --source github sync`) ya
corre el source de GitHub; los commits pasan por ahí en cuanto `include_commits: true`. El job
de embeddings (02:15) re-embebe después. **No toqué ninguno de los dos crons.** Cuando Discord
esté configurado, el mismo job `sync` (corrido sin `--source github`, es decir `omnia sync`)
corre las dos fuentes; ahí actualizamos el `ProgramArguments` del plist para sacar el filtro
`--source github`.

---

## Ideas para más fuentes (no construidas — decime si usás alguna)

- **Linear / Jira** — `meta.go` ya reserva un kind de source `jira`. Fácil de agregar si usás alguno.
- **Google Calendar** — eventos como observaciones (necesita configurar OAuth).
- **Notas locales / vault de Obsidian** — ingerir archivos markdown de una carpeta.
- **Diffs/stats por commit** — la ingesta actual guarda mensaje + autor; podríamos sumar
  archivos cambiados / líneas +− (una llamada extra a la API por commit; más pesado).

---

## Decisiones que necesito de vos

1. **Commits de GitHub:** ¿prendo `include_commits: true`? ¿Algún repo a *excluir*? ¿Qué
   `max_commits_per_repo` te parece bien para el primer backfill?
2. **Discord:** ¿querés ingesta de mensajes (ya construida, necesita token del bot + canales),
   un bot que responda (construcción aparte), o las dos cosas?
3. **WhatsApp:** ¿Opción A (export-import segura — mandame una muestra) u Opción B (no oficial,
   riesgo de ban — necesito tu visto bueno explícito)?
4. **¿Alguna otra fuente** de la lista de ideas?

Por ahora no hay nada activo salvo lo que ya venía corriendo (issues/PRs de GitHub). El código
nuevo de commits está commiteado pero apagado hasta que decidas.
