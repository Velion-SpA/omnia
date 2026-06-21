.PHONY: templ

# The unified dashboard (internal/dashboard) and the shared design system
# (internal/ui) own all templ components; the cloud reuses them via its data-source
# adapter rather than a separate templ tree.
templ:
	go run github.com/a-h/templ/cmd/templ@v0.3.1020 generate ./internal/dashboard/... ./internal/ui/...
