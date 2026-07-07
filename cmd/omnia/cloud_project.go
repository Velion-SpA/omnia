package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/velion/omnia/internal/envx"
	"github.com/velion/omnia/internal/store"
)

// cmdCloudProject implements `omnia cloud project <pause|resume> <project> [options]`.
//
// OBL-04: the server already ENFORCES a per-project sync pause (IsProjectSyncEnabled
// gates pushes with a 409 once paused), but until this command existed nothing could
// actually FLIP the switch except a raw HTTP call — SetProjectSyncEnabled had zero
// callers. Both subcommands hit the operator-gated POST /admin/projects/{project}/
// pause|resume routes (requireOperator — same gate as the rest of the Admin section),
// authenticating with the OMNIA_CLOUD_ADMIN credential rather than an account token,
// since pausing a project is an operator action, not a per-account one.
func cmdCloudProject(cfg store.Config) {
	if len(os.Args) < 4 {
		printCloudProjectUsage(os.Stderr)
		exitFunc(1)
		return
	}
	switch os.Args[3] {
	case "--help", "-h", "help":
		printCloudProjectUsage(os.Stdout)
		return
	case "pause":
		cmdCloudProjectSetSync(cfg, false)
	case "resume":
		cmdCloudProjectSetSync(cfg, true)
	default:
		fmt.Fprintf(os.Stderr, "unknown cloud project command: %s\n", os.Args[3])
		printCloudProjectUsage(os.Stderr)
		exitFunc(1)
	}
}

func printCloudProjectUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: omnia cloud project <pause|resume> <project> [options]")
	fmt.Fprintln(w, "  pause <project> [--reason \"...\"]  pause sync for a project (pushes are rejected with 409 until resumed)")
	fmt.Fprintln(w, "  resume <project>                   resume sync for a paused project")
	fmt.Fprintln(w, "options: --cloud-name <alias>  --server <url>  --admin-token <token>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "requires the operator admin credential: --admin-token, or OMNIA_CLOUD_ADMIN in the environment")
}

// cmdCloudProjectSetSync implements both `pause` (enabled=false) and `resume`
// (enabled=true) — they differ only in the HTTP path and whether a reason is sent.
func cmdCloudProjectSetSync(cfg store.Config, enabled bool) {
	args := os.Args[4:]
	project := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		project = strings.TrimSpace(args[0])
		args = args[1:]
	}
	action := "resume"
	if !enabled {
		action = "pause"
	}
	fs := flag.NewFlagSet("omnia cloud project "+action, flag.ContinueOnError)
	reason := fs.String("reason", "", "optional reason recorded with the pause (ignored for resume)")
	server := fs.String("server", "", "cloud server URL (overrides cloud.json)")
	cloudAlias := bindCloudNameFlag(fs, "cloud alias (default: default cloud)")
	adminToken := fs.String("admin-token", "", "operator admin credential (default: OMNIA_CLOUD_ADMIN env var)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	if project == "" {
		fmt.Fprintf(os.Stderr, "usage: omnia cloud project %s <project> [options]\n", action)
		fmt.Fprintln(os.Stderr, "error: a project name is required")
		exitFunc(1)
		return
	}

	token := strings.TrimSpace(*adminToken)
	if token == "" {
		token = strings.TrimSpace(envx.Get("OMNIA_CLOUD_ADMIN"))
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "error: an operator admin credential is required: pass --admin-token or set OMNIA_CLOUD_ADMIN")
		exitFunc(1)
		return
	}

	serverURL, err := resolveAdminServerURL(cfg, *server, strings.TrimSpace(*cloudAlias))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	var payload any
	if !enabled {
		payload = map[string]string{"paused_reason": strings.TrimSpace(*reason)}
	}

	resp, err := doAuthedCloudRequest(http.MethodPost, serverURL+"/admin/projects/"+url.PathEscape(project)+"/"+action, token, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s request failed: %v\n", action, err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		if enabled {
			fmt.Printf("Project %q sync resumed.\n", project)
		} else if r := strings.TrimSpace(*reason); r != "" {
			fmt.Printf("Project %q sync paused (reason: %s).\n", project, r)
		} else {
			fmt.Printf("Project %q sync paused.\n", project)
		}
	case http.StatusUnauthorized:
		fmt.Fprintln(os.Stderr, "error: unauthorized; check --admin-token / OMNIA_CLOUD_ADMIN")
		exitFunc(1)
	case http.StatusForbidden:
		fmt.Fprintln(os.Stderr, "error: forbidden; the operator admin credential is required")
		exitFunc(1)
	default:
		fmt.Fprintf(os.Stderr, "%s failed (%d): %s\n", action, resp.StatusCode, serverError(body))
		exitFunc(1)
	}
}

// resolveAdminServerURL resolves the cloud server URL for an operator admin
// action. Unlike resolveDeviceTarget, it does NOT require a stored account
// token — the admin credential is a separate, more privileged secret supplied
// via --admin-token or OMNIA_CLOUD_ADMIN, never persisted in cloud.json.
func resolveAdminServerURL(cfg store.Config, flagServer, alias string) (string, error) {
	v2, _ := loadCloudConfigV2(cfg) // best-effort; nil is fine
	if alias != "" && v2 != nil {
		if _, ok := v2.getCloud(alias); !ok {
			return "", fmt.Errorf("cloud %q not found; run `omnia cloud add %s --server <url>` first", alias, alias)
		}
	}
	return resolveCloudServerForAlias(flagServer, alias, v2)
}
