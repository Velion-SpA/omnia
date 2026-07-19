package i18n

import "fmt"

// messages is the dashboard's bilingual copy catalog, keyed by dot-separated
// identifiers. Slice 1 seeds ONLY the keys used by the shared shell
// (internal/ui/layout.templ, internal/dashboard/adminnav.go) and the
// Overview page (internal/dashboard/overview.templ, overview handler) —
// every other page keeps its existing English literals until a later slice.
//
// Every entry MUST carry both LangES and LangEN — TestAllCatalogEntriesHaveBothLanguages
// enforces this so T()'s fallback chain never has to paper over a gap.
var messages = map[string]map[Lang]string{
	// --- Shared shell (internal/ui/layout.templ) ---
	"shell.brand.sub":      {LangES: "Conocimiento Unificado", LangEN: "Unified Knowledge"},
	"shell.status.online":  {LangES: "En línea", LangEN: "Online"},
	"shell.status.offline": {LangES: "Desconectado", LangEN: "Offline"},
	"shell.logout":         {LangES: "Salir", LangEN: "Logout"},

	// --- Nav items (dashboard.BaseNavItems / AdminNavItem, keyed by NavItem.ID) ---
	"nav.overview": {LangES: "Resumen", LangEN: "Overview"},
	"nav.browse":   {LangES: "Explorar", LangEN: "Browse"},
	"nav.graph":    {LangES: "Grafo", LangEN: "Graph"},
	"nav.sync":     {LangES: "Sync", LangEN: "Sync"},
	"nav.activity": {LangES: "Actividad", LangEN: "Activity"},
	"nav.admin":    {LangES: "Admin", LangEN: "Admin"},

	// --- Overview page (internal/dashboard/overview.templ) ---
	"overview.pageTitle":        {LangES: "Resumen", LangEN: "Overview"},
	"overview.totalMemories":    {LangES: "Memorias Totales", LangEN: "Total Memories"},
	"overview.projects":         {LangES: "Proyectos", LangEN: "Projects"},
	"overview.lastSync":         {LangES: "Última Sincronización", LangEN: "Last Sync"},
	"overview.autoMode":         {LangES: "modo automático", LangEN: "auto-mode"},
	"overview.noSyncYet":        {LangES: "sin sincronizar aún", LangEN: "no sync yet"},
	"overview.knowledgeBases":   {LangES: "Bases de Conocimiento", LangEN: "Knowledge Bases"},
	"overview.sortedByCount":    {LangES: "ordenado por cantidad", LangEN: "sorted by count"},
	"overview.noProjectsPrefix": {LangES: "No se encontraron proyectos. Ejecutá ", LangEN: "No projects found. Run "},
	"overview.noProjectsSuffix": {LangES: " primero.", LangEN: " first."},
	"overview.freshnessFresh":   {LangES: "reciente", LangEN: "fresh"},
	"overview.freshnessReview":  {LangES: "revisar", LangEN: "review"},
	"overview.breakdown":        {LangES: "Desglose", LangEN: "Breakdown"},
	"overview.memoryTypes":      {LangES: "Tipos de Memoria", LangEN: "Memory Types"},
	"overview.noTypeData":       {LangES: "No hay datos de tipos disponibles.", LangEN: "No type data available."},
	"overview.recentIngest":     {LangES: "Ingesta Reciente", LangEN: "Recent Ingest"},
	"overview.liveFeed":         {LangES: "Feed en Vivo", LangEN: "Live Feed"},
	"overview.live":             {LangES: "En vivo", LangEN: "Live"},
	"overview.noRecentActivity": {LangES: "Sin actividad reciente.", LangEN: "No recent activity."},
	"overview.connected":        {LangES: "Conectado", LangEN: "Connected"},
	"overview.sources":          {LangES: "Fuentes", LangEN: "Sources"},

	// Overview "Sources" panel entries (internal/dashboard/handlers.go buildOverviewData).
	"overview.source.github.name":  {LangES: "GitHub", LangEN: "GitHub"},
	"overview.source.github.sub":   {LangES: "PRs · commits · revisiones", LangEN: "PRs · commits · reviews"},
	"overview.source.discord.name": {LangES: "Discord", LangEN: "Discord"},
	"overview.source.discord.sub":  {LangES: "resúmenes · hilos · menciones", LangEN: "digests · threads · mentions"},
	"overview.source.claude.name":  {LangES: "Claude Code", LangEN: "Claude Code"},
	"overview.source.claude.sub":   {LangES: "sesiones · fixes · decisiones", LangEN: "sessions · fixes · decisions"},

	// --- Shared "field" labels reused across multiple Slice 2 pages (detail,
	// activity, graph) — kept generic (not page-prefixed) precisely because
	// they recur verbatim across pages; see also "common.*" below. ---
	"field.project": {LangES: "Proyecto", LangEN: "Project"},

	// --- Shared cross-page action words (Slice 2) ---
	"common.cancel": {LangES: "Cancelar", LangEN: "Cancel"},

	// --- Relative time / "age" formatting (internal/dashboard's formatAge,
	// internal/ui's RelativeTimeLang — both describe how long ago something
	// happened, e.g. a memory's last update or a sync target's last-sync
	// time). Slice 2: previously hardcoded English regardless of locale. ---
	"age.unknown":    {LangES: "desconocido", LangEN: "unknown"},
	"age.justNow":    {LangES: "ahora mismo", LangEN: "just now"},
	"age.yesterday":  {LangES: "ayer", LangEN: "yesterday"},
	"age.minutesAgo": {LangES: "hace %dm", LangEN: "%dm ago"},
	"age.hoursAgo":   {LangES: "hace %dh", LangEN: "%dh ago"},
	"age.daysAgo":    {LangES: "hace %dd", LangEN: "%dd ago"},

	// --- Browse page (internal/dashboard/browse.templ + browse.go) ---
	"browse.pageTitle":         {LangES: "Explorar", LangEN: "Browse"},
	"browse.eyebrow":           {LangES: "base de conocimiento", LangEN: "knowledge base"},
	"browse.filters":           {LangES: "Filtros", LangEN: "Filters"},
	"browse.project":           {LangES: "Proyecto", LangEN: "Project"},
	"browse.projectAria":       {LangES: "Filtrar por proyecto", LangEN: "Filter by project"},
	"browse.allProjects":       {LangES: "Todos los proyectos", LangEN: "All projects"},
	"browse.go":                {LangES: "Ir", LangEN: "Go"},
	"browse.source":            {LangES: "Fuente", LangEN: "Source"},
	"browse.sourceAria":        {LangES: "Filtrar por fuente", LangEN: "Filter by source"},
	"browse.kind":              {LangES: "Formato", LangEN: "Kind"},
	"browse.kindAria":          {LangES: "Filtrar por formato", LangEN: "Filter by kind"},
	"browse.category":          {LangES: "Categoría", LangEN: "Category"},
	"browse.categoryAria":      {LangES: "Filtrar por categoría", LangEN: "Filter by category"},
	"browse.all":               {LangES: "Todos", LangEN: "All"},
	"browse.search":            {LangES: "Buscar", LangEN: "Search"},
	"browse.searchPlaceholder": {LangES: "Buscar observaciones...", LangEN: "Search observations..."},
	"browse.resultsCount":      {LangES: "%d resultados", LangEN: "%d results"},
	"browse.clearAll":          {LangES: "limpiar todo ×", LangEN: "clear all ×"},
	"browse.subProjects":       {LangES: "Sub-Proyectos", LangEN: "Sub-Projects"},
	"browse.subNavCore":        {LangES: "%s (núcleo)", LangEN: "%s (core)"},
	"browse.removeFilter":      {LangES: "Quitar filtro", LangEN: "Remove filter"},
	"browse.filterChipSource":  {LangES: "Fuente · %s", LangEN: "Source · %s"},
	"browse.filterChipKind":    {LangES: "Formato · %s", LangEN: "Kind · %s"},
	"browse.filterChipType":    {LangES: "Tipo · %s", LangEN: "Type · %s"},

	// --- Project detail page (internal/dashboard/projectdetail.templ + .go) ---
	"projectDetail.notFound":      {LangES: "No se seleccionó ningún proyecto. Elegí uno desde %s o %s.", LangEN: "No project selected. Pick a project from %s or %s."},
	"projectDetail.memories":      {LangES: "Memorias", LangEN: "Memories"},
	"projectDetail.lastActivity":  {LangES: "Última actividad", LangEN: "Last activity"},
	"projectDetail.recentCount":   {LangES: "%d recientes", LangEN: "%d recent"},
	"projectDetail.viewAllBrowse": {LangES: "Ver todo en Explorar", LangEN: "View all in Browse"},
	"projectDetail.subProjects":   {LangES: "Sub-proyectos", LangEN: "Sub-projects"},
	"projectDetail.fallbackTitle": {LangES: "Proyecto", LangEN: "Project"},

	// --- Detail page (internal/dashboard/detail.templ) ---
	"detail.badgeIngested":       {LangES: "ingerido", LangEN: "ingested"},
	"detail.badgeCurated":        {LangES: "curado", LangEN: "curated"},
	"detail.content":             {LangES: "Contenido", LangEN: "Content"},
	"detail.edit":                {LangES: "Editar", LangEN: "Edit"},
	"detail.editObservation":     {LangES: "Editar observación", LangEN: "Edit Observation"},
	"detail.titleLabel":          {LangES: "Título", LangEN: "Title"},
	"detail.typeLabel":           {LangES: "Tipo", LangEN: "Type"},
	"detail.save":                {LangES: "Guardar", LangEN: "Save"},
	"detail.savedSuccessfully":   {LangES: "Guardado correctamente.", LangEN: "Saved successfully."},
	"detail.editAgain":           {LangES: "Editar de nuevo", LangEN: "Edit again"},
	"detail.delete":              {LangES: "Eliminar", LangEN: "Delete"},
	"detail.deleteQuestion":      {LangES: "¿Eliminar observación?", LangEN: "Delete observation?"},
	"detail.deleteExplain":       {LangES: "El borrado suave la oculta. El borrado permanente no se puede deshacer.", LangEN: "Soft delete marks it hidden. Hard delete is permanent and cannot be undone."},
	"detail.softDelete":          {LangES: "Borrado suave", LangEN: "Soft delete"},
	"detail.hardDeletePermanent": {LangES: "Borrado permanente", LangEN: "Hard delete (permanent)"},
	"detail.hardDeleteConfirm":   {LangES: "¿Eliminar esta observación de forma permanente? No se puede deshacer.", LangEN: "Permanently delete this observation? This cannot be undone."},
	"detail.deletedPermanently":  {LangES: "Eliminado permanentemente.", LangEN: "Permanently deleted."},
	"detail.deletedSoft":         {LangES: "Borrado suave (oculto de los resultados).", LangEN: "Soft deleted (hidden from results)."},
	"detail.backToBrowse":        {LangES: "Volver a Explorar →", LangEN: "Back to browse →"},
	"detail.record":              {LangES: "REGISTRO", LangEN: "RECORD"},
	"detail.topic":               {LangES: "Tema", LangEN: "Topic"},
	"detail.revisions":           {LangES: "Revisiones", LangEN: "Revisions"},
	"detail.created":             {LangES: "Creado", LangEN: "Created"},
	"detail.updated":             {LangES: "Actualizado", LangEN: "Updated"},
	"detail.lastEdit":            {LangES: "Última edición", LangEN: "Last edit"},
	"detail.omniaMeta":           {LangES: "Metadatos de Omnia", LangEN: "Omnia Meta"},
	"detail.author":              {LangES: "Autor", LangEN: "Author"},
	"detail.participants":        {LangES: "Participantes", LangEN: "Participants"},
	"detail.sourceID":            {LangES: "ID de origen", LangEN: "Source ID"},
	"detail.ingestedAt":          {LangES: "Ingerido el", LangEN: "Ingested"},
	"detail.chunk":               {LangES: "Fragmento", LangEN: "Chunk"},

	// --- Sync page (internal/dashboard/syncpage.templ + syncstatus.go) ---
	"sync.pageTitle":          {LangES: "Estado de Sincronización", LangEN: "Sync Status"},
	"sync.targets":            {LangES: "OBJETIVOS DE SINCRONIZACIÓN", LangEN: "SYNC TARGETS"},
	"sync.noTargetsPrefix":    {LangES: "Aún no hay objetivos de sincronización en la nube registrados. Ejecutá ", LangEN: "No cloud sync targets recorded yet. Run "},
	"sync.noTargetsSuffix":    {LangES: " para empezar a replicarlo.", LangEN: " to start replicating one."},
	"sync.cursors":            {LangES: "CURSORES DE SINCRONIZACIÓN", LangEN: "SYNC CURSORS"},
	"sync.stateMissingPrefix": {LangES: "No se encontró el archivo de estado. Ejecutá ", LangEN: "State file not found. Run "},
	"sync.stateMissingSuffix": {LangES: " para crearlo.", LangEN: " to create it."},
	"sync.noCursorsYet":       {LangES: "Aún no hay cursores registrados.", LangEN: "No cursors recorded yet."},
	"sync.log":                {LangES: "REGISTRO DE SINCRONIZACIÓN", LangEN: "SYNC LOG"},
	"sync.noLogFile":          {LangES: "No se encontró archivo de registro. Omnia solo registra en stderr (no hay un logger de archivo configurado en v1).", LangEN: "No log file found. Omnia logs to stderr only (no file logger configured in v1)."},
	"sync.health.unknown":     {LangES: "desconocido", LangEN: "unknown"},
	"sync.health.healthy":     {LangES: "saludable", LangEN: "healthy"},
	"sync.health.pending":     {LangES: "pendiente", LangEN: "pending"},
	"sync.health.running":     {LangES: "en ejecución", LangEN: "running"},
	"sync.health.degraded":    {LangES: "degradado", LangEN: "degraded"},

	// --- Activity page (internal/dashboard/activity.templ) ---
	"activity.pageTitle":  {LangES: "Registro de Actividad", LangEN: "Activity Log"},
	"activity.emptyState": {LangES: "Aún no hay actividad registrada. Las ediciones y eliminaciones aparecerán acá.", LangEN: "No activity recorded yet. Edits and deletes will appear here."},
	"activity.time":       {LangES: "Hora", LangEN: "Time"},
	"activity.action":     {LangES: "Acción", LangEN: "Action"},
	"activity.summary":    {LangES: "Descripción", LangEN: "Summary"},
	"activity.result":     {LangES: "Resultado", LangEN: "Result"},

	// --- Graph page (internal/dashboard/graph.templ) ---
	"graph.unavailableTitle": {LangES: "Grafo Semántico No Disponible", LangEN: "Semantic Graph Unavailable"},
	"graph.unavailablePart1": {LangES: "Esta vista se construye a partir de similitud semántica REAL entre memorias, lo cual requiere la capa local de embeddings de Omnia. Habilitá ", LangEN: "This view is built from REAL semantic similarity between memories, which requires Omnia's local embeddings layer. Enable "},
	"graph.unavailablePart2": {LangES: " en tu configuración y ejecutá ", LangEN: " in your config and run "},
	"graph.unavailablePart3": {LangES: " para poblar el almacén de vectores y luego recargá esta página.", LangEN: " to populate the vector store, then reload this page."},
	"graph.backToOverview":   {LangES: "← Volver a Resumen", LangEN: "← Back to Overview"},
	"graph.neighborsK":       {LangES: "Vecinos (k)", LangEN: "Neighbors (k)"},
	"graph.scopeAria":        {LangES: "Limitar el grafo a un proyecto", LangEN: "Scope graph to project"},
	"graph.minSimilarity":    {LangES: "Similitud mínima", LangEN: "Min similarity"},
	"graph.apply":            {LangES: "Aplicar", LangEN: "Apply"},
	"graph.hint":             {LangES: "Bajá el umbral o subí k para un grafo más denso; subí el umbral para grupos más ajustados y de mayor confianza.", LangEN: "Lower the threshold or raise k for a denser graph; raise the threshold for tighter, higher-confidence clusters."},
	"graph.overlayEyebrow":   {LangES: "Grafo de Conocimiento", LangEN: "Knowledge Graph"},
	"graph.connected":        {LangES: "conectadas", LangEN: "connected"},
	"graph.links":            {LangES: "enlaces", LangEN: "links"},
	"graph.memories":         {LangES: "memorias", LangEN: "memories"},
	"graph.noteSeg1":         {LangES: "Los enlaces son similitud coseno ≥ ", LangEN: "Edges = cosine similarity ≥ "},
	"graph.noteSeg2":         {LangES: " desde los embeddings locales · top-%d vecinos. ", LangEN: " from local embeddings · top-%d neighbors. "},
	"graph.noteSeg3":         {LangES: " memorias no tienen enlace en este umbral.", LangEN: " memories have no link at this threshold."},
	"graph.fitView":          {LangES: "Restablecer / ajustar vista", LangEN: "Reset / fit view"},
	"graph.fitViewAria":      {LangES: "Ajustar vista", LangEN: "Fit view"},
	"graph.toggleLabels":     {LangES: "Alternar etiquetas de nodos clave", LangEN: "Toggle hub labels"},
	"graph.toggleLabelsAria": {LangES: "Alternar etiquetas", LangEN: "Toggle labels"},
	"graph.noLinksTitle":     {LangES: "Sin enlaces semánticos", LangEN: "No semantic links"},
	"graph.noLinksPrefix":    {LangES: "Ningún par de memorias alcanza el umbral de similitud actual en este alcance. Bajá la ", LangEN: "No pair of memories meets the current similarity threshold in this scope. Lower the "},
	"graph.noLinksSuffix":    {LangES: " o ampliá el filtro de proyecto y aplicá.", LangEN: " or widen the project filter, then Apply."},
	// JS-side strings (Slice 2): passed to the client via @templ.JSONScript
	// ("graph-i18n") so the D3 tooltip/legend logic (a <script> block, which
	// cannot call ui.T at runtime) can render in the current locale too.
	"graph.js.connectionSingular": {LangES: "conexión", LangEN: "connection"},
	"graph.js.connectionPlural":   {LangES: "conexiones", LangEN: "connections"},
	"graph.js.connectsTo":         {LangES: "Conecta con", LangEN: "Connects to"},
	"graph.js.noLinksAtThreshold": {LangES: "Sin enlaces en este umbral", LangEN: "No links at this threshold"},
	"graph.js.moreProjectsFmt":    {LangES: "+ {n} proyectos más", LangEN: "+ {n} more projects"},

	// --- Shared UI components (internal/ui/cards.templ) ---
	"cards.manual":             {LangES: "Manual", LangEN: "Manual"},
	"cards.emptySignal":        {LangES: "SIN SEÑAL AÚN", LangEN: "NO SIGNAL YET"},
	"cards.noResults":          {LangES: "Sin resultados", LangEN: "No results"},
	"cards.noResultsHint":      {LangES: "No se encontraron observaciones. Probá con otro filtro o término de búsqueda.", LangEN: "No observations found. Try a different filter or search term."},
	"cards.view":               {LangES: "Vista", LangEN: "View"},
	"cards.densityComfortable": {LangES: "cómoda", LangEN: "comfortable"},
	"cards.densityCompact":     {LangES: "compacta", LangEN: "compact"},

	// --- i18n Slice 3: cloud Admin section (internal/cloud/cloudserver) ---

	// Shared Admin shell (admin_ui.templ / admin_dashboard.go)
	"admin.title":         {LangES: "Admin", LangEN: "Admin"},
	"admin.operatorLabel": {LangES: "operador", LangEN: "operator"},

	// Admin nav tabs (adminNavTabs)
	"admin.tabs.users":    {LangES: "Usuarios", LangEN: "Users"},
	"admin.tabs.access":   {LangES: "Accesos", LangEN: "Access"},
	"admin.tabs.teams":    {LangES: "Equipos", LangEN: "Teams"},
	"admin.tabs.profiles": {LangES: "Perfiles", LangEN: "Profiles"},
	"admin.tabs.projects": {LangES: "Proyectos", LangEN: "Projects"},
	"admin.tabs.audit":    {LangES: "Auditoría", LangEN: "Audit"},

	// Admin > Users page
	"admin.users.subtitle":                {LangES: "Gestioná cuentas, tokens y accesos. Solo para operadores.", LangEN: "Manage accounts, tokens, and access. Operator-only."},
	"admin.users.searchPlaceholder":       {LangES: "Buscar usuarios…", LangEN: "Search users…"},
	"admin.users.newUser":                 {LangES: "Nuevo usuario", LangEN: "New user"},
	"admin.users.noneYet":                 {LangES: "Aún no hay cuentas.", LangEN: "No accounts yet."},
	"admin.users.label":                   {LangES: "USUARIOS", LangEN: "USERS"},
	"admin.users.colUsername":             {LangES: "Usuario", LangEN: "Username"},
	"admin.users.colEmail":                {LangES: "Email", LangEN: "Email"},
	"admin.users.colStatus":               {LangES: "Estado", LangEN: "Status"},
	"admin.users.colCreated":              {LangES: "Creado", LangEN: "Created"},
	"admin.users.colTokens":               {LangES: "Tokens", LangEN: "Tokens"},
	"admin.users.colLastTokenUse":         {LangES: "Último uso de token", LangEN: "Last token use"},
	"admin.users.colActions":              {LangES: "Acciones", LangEN: "Actions"},
	"admin.users.statusDisabled":          {LangES: "deshabilitado", LangEN: "disabled"},
	"admin.users.statusActive":            {LangES: "activo", LangEN: "active"},
	"admin.users.accessLink":              {LangES: "Acceso", LangEN: "Access"},
	"admin.users.manageAccess":            {LangES: "Gestionar acceso", LangEN: "Manage access"},
	"admin.users.editUser":                {LangES: "Editar usuario", LangEN: "Edit user"},
	"admin.users.resetPassword":           {LangES: "Restablecer contraseña", LangEN: "Reset password"},
	"admin.users.issueToken":              {LangES: "Emitir token", LangEN: "Issue token"},
	"admin.users.tokenLabelPlaceholder":   {LangES: "etiqueta", LangEN: "label"},
	"admin.users.go":                      {LangES: "Ir", LangEN: "Go"},
	"admin.users.demote":                  {LangES: "Quitar admin", LangEN: "Demote"},
	"admin.users.promote":                 {LangES: "Ascender a admin", LangEN: "Promote to admin"},
	"admin.users.enable":                  {LangES: "Habilitar", LangEN: "Enable"},
	"admin.users.disable":                 {LangES: "Deshabilitar", LangEN: "Disable"},
	"admin.users.disableConfirm":          {LangES: "¿Deshabilitar este usuario? No podrá volver a iniciar sesión.", LangEN: "Disable this user? They will no longer be able to sign in."},
	"admin.users.deleteEllipsis":          {LangES: "Eliminar…", LangEN: "Delete…"},
	"admin.users.deleteTitle":             {LangES: "¿Eliminar %s?", LangEN: "Delete %s?"},
	"admin.users.deleteNote":              {LangES: "Esto elimina permanentemente la cuenta y todos sus datos — membresías, tokens, dispositivos, membresías de equipo. No se puede deshacer.", LangEN: "This permanently deletes the account and all its data — memberships, tokens, devices, team memberships. This cannot be undone."},
	"admin.users.deletePermanently":       {LangES: "Eliminar permanentemente", LangEN: "Delete permanently"},
	"admin.users.deleteConfirmPrompt":     {LangES: "¿Eliminar este usuario de forma permanente? No se puede deshacer.", LangEN: "Permanently delete this user? This cannot be undone."},
	"admin.users.fieldUsername":           {LangES: "Usuario", LangEN: "Username"},
	"admin.users.fieldEmail":              {LangES: "Email", LangEN: "Email"},
	"admin.users.fieldRole":               {LangES: "Rol", LangEN: "Role"},
	"admin.users.roleMember":              {LangES: "Miembro", LangEN: "Member"},
	"admin.users.roleAdmin":               {LangES: "Admin", LangEN: "Admin"},
	"admin.users.createUser":              {LangES: "Crear usuario", LangEN: "Create user"},
	"admin.users.saveChanges":             {LangES: "Guardar cambios", LangEN: "Save changes"},
	"admin.users.newPasswordOptional":     {LangES: "Nueva contraseña (opcional)", LangEN: "New password (optional)"},
	"admin.users.autoGeneratePlaceholder": {LangES: "Dejar en blanco para generar una automáticamente", LangEN: "Leave blank to auto-generate"},
	"admin.users.autoGenerateHint":        {LangES: "Dejalo en blanco para generar una contraseña aleatoria segura, que se muestra una sola vez.", LangEN: "Leave blank to generate a strong random password, shown once."},
	"admin.users.noTokensIssued":          {LangES: "No se emitieron tokens.", LangEN: "No tokens issued."},
	"admin.users.tokenRevoked":            {LangES: "revocado", LangEN: "revoked"},
	"admin.users.tokenLive":               {LangES: "activo", LangEN: "live"},
	"admin.users.tokenCreated":            {LangES: "creado", LangEN: "created"},
	"admin.users.tokenLastUsed":           {LangES: "último uso", LangEN: "last used"},
	"admin.users.revoke":                  {LangES: "Revocar", LangEN: "Revoke"},
	"admin.users.noLabel":                 {LangES: "(sin etiqueta)", LangEN: "(no label)"},
	"admin.users.newTokenFor":             {LangES: "NUEVO TOKEN PARA %s", LangEN: "NEW TOKEN FOR %s"},
	"admin.users.copyNowWarning":          {LangES: "Copialo ahora — esta es la única vez que se muestra.", LangEN: "Copy it now — this is the only time it is shown."},
	"admin.users.done":                    {LangES: "Listo", LangEN: "Done"},
	"admin.users.countAccountSingular":    {LangES: "cuenta", LangEN: "account"},
	"admin.users.countAccountPlural":      {LangES: "cuentas", LangEN: "accounts"},
	"admin.users.countAdminSingular":      {LangES: "admin", LangEN: "admin"},
	"admin.users.countAdminPlural":        {LangES: "admins", LangEN: "admins"},

	// Admin > Access page
	"admin.access.subtitle":                  {LangES: "Acceso efectivo para una cuenta. Un override siempre gana sobre los permisos derivados de equipo.", LangEN: "Effective access for one account. An override always wins over team-derived perms."},
	"admin.access.accountLabel":              {LangES: "CUENTA", LangEN: "ACCOUNT"},
	"admin.access.effectiveAccess":           {LangES: "ACCESO EFECTIVO", LangEN: "EFFECTIVE ACCESS"},
	"admin.access.colProject":                {LangES: "Proyecto", LangEN: "Project"},
	"admin.access.colAccess":                 {LangES: "Acceso", LangEN: "Access"},
	"admin.access.colSource":                 {LangES: "Origen", LangEN: "Source"},
	"admin.access.colEdit":                   {LangES: "Editar", LangEN: "Edit"},
	"admin.access.noKnownProjects":           {LangES: "Aún no hay proyectos conocidos.", LangEN: "No known projects yet."},
	"admin.access.searchAccountsPlaceholder": {LangES: "Buscar cuentas…", LangEN: "Search accounts…"},
	"admin.access.none":                      {LangES: "Ninguno", LangEN: "None"},
	"admin.access.full":                      {LangES: "Total", LangEN: "Full"},
	"admin.access.read":                      {LangES: "Lectura", LangEN: "Read"},
	"admin.access.partial":                   {LangES: "Parcial", LangEN: "Partial"},
	"admin.access.edit":                      {LangES: "Editar", LangEN: "Edit"},
	"admin.access.revoke":                    {LangES: "Revocar", LangEN: "Revoke"},
	"admin.access.overrideEllipsis":          {LangES: "Anular…", LangEN: "Override…"},
	"admin.access.grantEllipsis":             {LangES: "Otorgar…", LangEN: "Grant…"},
	"admin.access.override":                  {LangES: "Override", LangEN: "Override"},
	"admin.access.team":                      {LangES: "Equipo", LangEN: "Team"},
	"admin.access.save":                      {LangES: "Guardar", LangEN: "Save"},
	"admin.access.revokeTitle":               {LangES: "¿Revocar el acceso a %s?", LangEN: "Revoke access to %s?"},
	"admin.access.revokeNote":                {LangES: "Elimina el override de esta cuenta. Vuelve al acceso derivado del equipo, si existe, para este proyecto.", LangEN: "Removes this account's override. It falls back to team-derived access, if any, for this project."},
	"admin.access.revokeConfirmPrompt":       {LangES: "¿Revocar el acceso a %s para esta cuenta?", LangEN: "Revoke access to %s for this account?"},

	// Permission summary phrases (permSummary — admin_dashboard.go)
	"admin.perm.noAccess": {LangES: "sin acceso", LangEN: "no access"},
	"admin.perm.full":     {LangES: "total (lectura+escritura+actualización+borrado)", LangEN: "full (read+write+update+delete)"},
	"admin.perm.read":     {LangES: "lectura", LangEN: "read"},
	"admin.perm.write":    {LangES: "escritura", LangEN: "write"},
	"admin.perm.update":   {LangES: "actualización", LangEN: "update"},
	"admin.perm.delete":   {LangES: "borrado", LangEN: "delete"},
	"admin.perm.readOnly": {LangES: "solo lectura", LangEN: "read-only"},

	// Admin > Projects page
	"admin.projects.subtitle":               {LangES: "Proyectos: contenido, clasificación y sincronización. Solo para operadores.", LangEN: "Projects: content, classification, and sync. Operator-only."},
	"admin.projects.noneKnownYet":           {LangES: "Aún no hay proyectos conocidos.", LangEN: "No projects known yet."},
	"admin.projects.suggestedLinks":         {LangES: "ENLACES SUGERIDOS", LangEN: "SUGGESTED LINKS"},
	"admin.projects.suggestedPrefix":        {LangES: "Sugerido: enlazar", LangEN: "Suggested: link"},
	"admin.projects.suggestedUnder":         {LangES: "bajo", LangEN: "under"},
	"admin.projects.confirm":                {LangES: "Confirmar", LangEN: "Confirm"},
	"admin.projects.dismiss":                {LangES: "Descartar", LangEN: "Dismiss"},
	"admin.projects.renameClassify":         {LangES: "Renombrar / clasificar", LangEN: "Rename / classify"},
	"admin.projects.displayNamePlaceholder": {LangES: "nombre visible (opcional)", LangEN: "display name (optional)"},
	"admin.projects.save":                   {LangES: "Guardar", LangEN: "Save"},
	"admin.projects.statMemories":           {LangES: "Memorias", LangEN: "Memories"},
	"admin.projects.statWithAccess":         {LangES: "Con acceso", LangEN: "With access"},
	"admin.projects.statSources":            {LangES: "Fuentes", LangEN: "Sources"},
	"admin.projects.statLast":               {LangES: "Última", LangEN: "Last"},
	"admin.projects.sync":                   {LangES: "Sync", LangEN: "Sync"},
	"admin.projects.paused":                 {LangES: "Pausado", LangEN: "Paused"},
	"admin.projects.subProjectOf":           {LangES: "sub-proyecto de %s", LangEN: "sub-project of %s"},
	"admin.projects.subProjectSingular":     {LangES: "sub-proyecto", LangEN: "sub-project"},
	"admin.projects.subProjectPlural":       {LangES: "sub-proyectos", LangEN: "sub-projects"},
	"admin.projects.countSingular":          {LangES: "proyecto", LangEN: "project"},
	"admin.projects.countPlural":            {LangES: "proyectos", LangEN: "projects"},
	"admin.projects.memories":               {LangES: "memorias", LangEN: "memories"},
	"admin.projects.unlinkFrom":             {LangES: "Desvincular de %s", LangEN: "Unlink from %s"},
	"admin.projects.linkToParent":           {LangES: "Enlazar a proyecto padre…", LangEN: "Link to parent project…"},
	"admin.projects.chooseParent":           {LangES: "Elegir padre…", LangEN: "Choose parent…"},
	"admin.projects.link":                   {LangES: "Enlazar", LangEN: "Link"},
	"admin.projects.pauseReasonPlaceholder": {LangES: "motivo de la pausa (opcional)", LangEN: "pause reason (optional)"},
	"admin.projects.pauseSync":              {LangES: "Pausar sincronización", LangEN: "Pause sync"},
	"admin.projects.resumeSync":             {LangES: "Reanudar sincronización", LangEN: "Resume sync"},
	"admin.projects.whoHasAccess":           {LangES: "QUIÉN TIENE ACCESO", LangEN: "WHO HAS ACCESS"},
	"admin.projects.noAccountsWithAccess":   {LangES: "Ninguna cuenta tiene acceso todavía.", LangEN: "No accounts have access yet."},

	// Kind badges (kindBadge/adminKindSelect — admin_teams_ui.templ)
	"admin.kind.work":         {LangES: "laboral", LangEN: "work"},
	"admin.kind.personal":     {LangES: "personal", LangEN: "personal"},
	"admin.kind.unclassified": {LangES: "sin clasificar", LangEN: "unclassified"},

	// Admin > Profiles page
	"admin.profiles.subtitle":        {LangES: "Los perfiles son presets de permisos aplicados a los proyectos de un equipo. Solo para operadores.", LangEN: "Profiles are permission presets applied to a team's projects. Operator-only."},
	"admin.profiles.label":           {LangES: "PERFILES", LangEN: "PROFILES"},
	"admin.profiles.noneYet":         {LangES: "Aún no hay perfiles. Creá uno abajo.", LangEN: "No profiles yet. Create one below."},
	"admin.profiles.save":            {LangES: "Guardar", LangEN: "Save"},
	"admin.profiles.deleteConflict":  {LangES: "Este perfil todavía está asignado a miembros del equipo. Reasignalos a otro perfil antes de eliminarlo.", LangEN: "This profile is still assigned to team members. Reassign them to another profile first, then delete."},
	"admin.profiles.delete":          {LangES: "Eliminar", LangEN: "Delete"},
	"admin.profiles.newProfile":      {LangES: "NUEVO PERFIL", LangEN: "NEW PROFILE"},
	"admin.profiles.namePlaceholder": {LangES: "nombre del perfil", LangEN: "profile name"},
	"admin.profiles.create":          {LangES: "Crear", LangEN: "Create"},

	// Admin > Teams page (+ detail)
	"admin.teams.subtitle":                  {LangES: "Los equipos agrupan proyectos y otorgan los permisos de un perfil a sus miembros. Solo para operadores.", LangEN: "Teams group projects and grant a profile's perms to members. Operator-only."},
	"admin.teams.newTeam":                   {LangES: "NUEVO EQUIPO", LangEN: "NEW TEAM"},
	"admin.teams.namePlaceholder":           {LangES: "nombre del equipo", LangEN: "team name"},
	"admin.teams.create":                    {LangES: "Crear", LangEN: "Create"},
	"admin.teams.personalGroup":             {LangES: "Equipos personales", LangEN: "Personal teams"},
	"admin.teams.workGroup":                 {LangES: "Equipos de trabajo", LangEN: "Work teams"},
	"admin.teams.noneYet":                   {LangES: "Ninguno todavía.", LangEN: "None yet."},
	"admin.teams.projectsMembersSummary":    {LangES: "%s proyectos · %s miembros", LangEN: "%s projects · %s members"},
	"admin.teams.manage":                    {LangES: "Gestionar", LangEN: "Manage"},
	"admin.teams.delete":                    {LangES: "Eliminar", LangEN: "Delete"},
	"admin.teams.detailSubtitle":            {LangES: "Proyectos y miembros del equipo. Solo para operadores.", LangEN: "Team projects and members. Operator-only."},
	"admin.teams.allTeams":                  {LangES: "← Todos los equipos", LangEN: "← All teams"},
	"admin.teams.teamLabel":                 {LangES: "EQUIPO", LangEN: "TEAM"},
	"admin.teams.save":                      {LangES: "Guardar", LangEN: "Save"},
	"admin.teams.deleteTeam":                {LangES: "Eliminar equipo", LangEN: "Delete team"},
	"admin.teams.noProjectsAddOne":          {LangES: "Sin proyectos. Agregá uno abajo.", LangEN: "No projects. Add one below."},
	"admin.teams.searchProjectsPlaceholder": {LangES: "Buscar proyectos para agregar…", LangEN: "Search projects to add…"},
	"admin.teams.addProject":                {LangES: "Agregar proyecto", LangEN: "Add project"},
	"admin.teams.membersLabel":              {LangES: "MIEMBROS", LangEN: "MEMBERS"},
	"admin.teams.noMembersAddOne":           {LangES: "Sin miembros. Agregá uno abajo.", LangEN: "No members. Add one below."},
	"admin.teams.addMember":                 {LangES: "Agregar miembro", LangEN: "Add member"},
	"admin.teams.remove":                    {LangES: "Quitar", LangEN: "Remove"},

	// Admin > Audit page
	"admin.audit.subtitle":       {LangES: "Registro de auditoría de seguridad, solo lectura. Solo para operadores.", LangEN: "Read-only security audit trail. Operator-only."},
	"admin.audit.filter":         {LangES: "FILTRO", LangEN: "FILTER"},
	"admin.audit.contributor":    {LangES: "colaborador", LangEN: "contributor"},
	"admin.audit.outcome":        {LangES: "resultado", LangEN: "outcome"},
	"admin.audit.filterAction":   {LangES: "Filtrar", LangEN: "Filter"},
	"admin.audit.clear":          {LangES: "Limpiar", LangEN: "Clear"},
	"admin.audit.log":            {LangES: "REGISTRO DE AUDITORÍA", LangEN: "AUDIT LOG"},
	"admin.audit.noEntries":      {LangES: "Ninguna entrada de auditoría coincide con este filtro.", LangEN: "No audit entries match this filter."},
	"admin.audit.colOccurred":    {LangES: "Ocurrió", LangEN: "Occurred"},
	"admin.audit.colContributor": {LangES: "Colaborador", LangEN: "Contributor"},
	"admin.audit.colAction":      {LangES: "Acción", LangEN: "Action"},
	"admin.audit.colOutcome":     {LangES: "Resultado", LangEN: "Outcome"},
	"admin.audit.colReason":      {LangES: "Motivo", LangEN: "Reason"},
	"admin.audit.pageOf":         {LangES: "Página %s de %s", LangEN: "Page %s of %s"},
	"admin.audit.prev":           {LangES: "Anterior", LangEN: "Prev"},
	"admin.audit.next":           {LangES: "Siguiente", LangEN: "Next"},
	"admin.audit.entrySingular":  {LangES: "%d entrada", LangEN: "%d entry"},
	"admin.audit.entryPlural":    {LangES: "%d entradas", LangEN: "%d entries"},

	// --- i18n Slice 3: cloud login page (internal/cloud/cloudserver/dashboard_mount.go, renderDashboardLoginPage) ---
	"auth.login.title":                    {LangES: "Iniciar sesión", LangEN: "Sign In"},
	"auth.login.kickerActive":             {LangES: "NUBE ACTIVA", LangEN: "CLOUD ACTIVE"},
	"auth.login.lead":                     {LangES: "Tu memoria compartida, con alcance a tu cuenta. Iniciá sesión para ver los proyectos a los que pertenecés — y nada más.", LangEN: "Your shared memory, scoped to your account. Sign in to see the projects you belong to — and nothing else."},
	"auth.login.consoleIdentityKey":       {LangES: "identidad", LangEN: "identity"},
	"auth.login.consoleIdentityValue":     {LangES: "acceso por cuenta", LangEN: "per-account access"},
	"auth.login.consoleScopeKey":          {LangES: "alcance", LangEN: "scope"},
	"auth.login.consoleScopeValue":        {LangES: "proyectos por membresía", LangEN: "projects by membership"},
	"auth.login.consoleModelKey":          {LangES: "modelo", LangEN: "model"},
	"auth.login.consoleModelValue":        {LangES: "local-first / con políticas de nube", LangEN: "local-first / cloud policy aware"},
	"auth.login.kickerSignIn":             {LangES: "INICIAR SESIÓN", LangEN: "SIGN IN"},
	"auth.login.heading":                  {LangES: "Iniciar sesión", LangEN: "Sign In"},
	"auth.login.copy":                     {LangES: "Usá las credenciales de tu cuenta para abrir una sesión del panel.", LangEN: "Use your account credentials to open a signed dashboard session."},
	"auth.login.usernameLabel":            {LangES: "Usuario", LangEN: "Username"},
	"auth.login.usernamePlaceholder":      {LangES: "usuario", LangEN: "username"},
	"auth.login.passwordLabel":            {LangES: "Contraseña", LangEN: "Password"},
	"auth.login.passwordPlaceholder":      {LangES: "contraseña", LangEN: "password"},
	"auth.login.submit":                   {LangES: "Iniciar sesión", LangEN: "Sign In"},
	"auth.login.operatorSummary":          {LangES: "Iniciar sesión como operador del servidor", LangEN: "Sign in as server operator"},
	"auth.login.operatorTokenLabel":       {LangES: "Token de operador", LangEN: "Operator token"},
	"auth.login.operatorTokenPlaceholder": {LangES: "token de operador de la nube", LangEN: "cloud operator token"},
	"auth.login.operatorSubmit":           {LangES: "Iniciar sesión como operador", LangEN: "Sign In as Operator"},
	"auth.login.errorInvalidCredentials":  {LangES: "usuario o contraseña inválidos", LangEN: "invalid username or password"},
	"auth.login.errorMissingCredentials":  {LangES: "ingresá las credenciales de tu cuenta o un token de operador", LangEN: "enter your account credentials or an operator token"},
	"auth.login.errorInvalidToken":        {LangES: "token inválido", LangEN: "invalid token"},
}

// T resolves key in the requested lang. Fallback chain: requested lang ->
// English -> the key itself (so a missing translation is visible in the UI,
// never a blank string). See catalog_test.go for the full fallback matrix.
func T(lang Lang, key string) string {
	entry, ok := messages[key]
	if !ok {
		return key
	}
	if v, ok := entry[lang]; ok {
		return v
	}
	if v, ok := entry[LangEN]; ok {
		return v
	}
	return key
}

// Tf is T with Sprintf-style interpolation for copy that embeds dynamic
// values (e.g. counts, names).
func Tf(lang Lang, key string, a ...any) string {
	return fmt.Sprintf(T(lang, key), a...)
}
