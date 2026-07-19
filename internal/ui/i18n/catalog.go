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
