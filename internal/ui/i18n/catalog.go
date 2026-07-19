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
