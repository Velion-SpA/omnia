package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Velion-SpA/omnia/internal/cloud"
	"github.com/Velion-SpA/omnia/internal/cloud/auth"
	"github.com/Velion-SpA/omnia/internal/cloud/cloudstore"
	"github.com/Velion-SpA/omnia/internal/envx"
	"github.com/Velion-SpA/omnia/internal/store"
)

// errBootstrapAdminExists is returned when a first-admin bootstrap is attempted
// against a store that already has at least one account. Bootstrap is one-time.
var errBootstrapAdminExists = errors.New(
	"an account already exists — first-admin bootstrap is one-time; " +
		"provision further accounts via the operator admin API (POST /admin/tokens, " +
		"/admin/users/{id}/disable, /admin/users/{id}/enable) instead",
)

// bootstrapAdminStore is the store seam used by the first-admin bootstrap: it needs
// to know whether any account already exists and to stamp the created account with
// the account-level admin flag (OBL-16).
type bootstrapAdminStore interface {
	HasAnyUser(ctx context.Context) (bool, error)
	SetUserAdmin(ctx context.Context, accountID string, admin bool) error
}

// bootstrapAdminSignup creates the account. *auth.Service satisfies it (bcrypt +
// validation live there); a fake is injected in tests.
type bootstrapAdminSignup interface {
	Signup(username, email, password string) (*cloudstore.User, error)
}

// bootstrapTokenIssuer mints the optional managed token (OBL-01 issuance reuse).
type bootstrapTokenIssuer interface {
	IssueManagedToken(ctx context.Context, userID, label string) (rawToken, tokenID string, err error)
}

type bootstrapAdminOptions struct {
	Username   string
	Email      string
	Password   string
	IssueToken bool
}

type bootstrapAdminResult struct {
	UserID   string
	Username string
	RawToken string
	TokenID  string
}

// runBootstrapAdmin is the transport-agnostic core of `omnia cloud bootstrap-admin`.
// It refuses to run once any account exists (idempotent-safe), creates the first
// admin account, and — when requested — issues a managed token exactly once by
// delegating to the OBL-01 issuance path.
func runBootstrapAdmin(ctx context.Context, cs bootstrapAdminStore, signup bootstrapAdminSignup, issuer bootstrapTokenIssuer, opts bootstrapAdminOptions) (bootstrapAdminResult, error) {
	exists, err := cs.HasAnyUser(ctx)
	if err != nil {
		return bootstrapAdminResult{}, err
	}
	if exists {
		return bootstrapAdminResult{}, errBootstrapAdminExists
	}
	user, err := signup.Signup(opts.Username, opts.Email, opts.Password)
	if err != nil {
		return bootstrapAdminResult{}, err
	}
	// OBL-16: the first admin is a REAL account admin — logging in with
	// username/password grants operator, independent of the OMNIA_CLOUD_ADMIN token.
	if err := cs.SetUserAdmin(ctx, user.ID, true); err != nil {
		return bootstrapAdminResult{}, fmt.Errorf("set admin flag: %w", err)
	}
	res := bootstrapAdminResult{UserID: user.ID, Username: user.Username}
	if opts.IssueToken {
		if issuer == nil {
			return res, fmt.Errorf("managed-token issuance is unavailable")
		}
		raw, tokenID, err := issuer.IssueManagedToken(ctx, user.ID, "bootstrap-admin")
		if err != nil {
			return res, fmt.Errorf("issue managed token: %w", err)
		}
		res.RawToken = raw
		res.TokenID = tokenID
	}
	return res, nil
}

// newBootstrapDeps builds the real store + auth service for bootstrap-admin. It is
// a package var so tests can inject fakes without a live Postgres.
var newBootstrapDeps = func(cfg cloud.Config, enableTokens bool) (cs bootstrapAdminStore, signup bootstrapAdminSignup, issuer bootstrapTokenIssuer, closer func() error, err error) {
	store, err := cloudstore.New(cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	authSvc, err := auth.NewService(store, cfg.JWTSecret)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, err
	}
	if enableTokens {
		authSvc.SetTokenPepper(envx.Get("OMNIA_CLOUD_TOKEN_PEPPER"))
	}
	return store, authSvc, authSvc, store.Close, nil
}

// cmdCloudBootstrapAdmin implements `omnia cloud bootstrap-admin`.
//
// It runs against the server's OWN storage (the same OMNIA_DATABASE_URL the server
// uses), NOT over HTTP, so an operator can seed the first admin on the server host
// without opening the public signup endpoint.
func cmdCloudBootstrapAdmin(cfg store.Config) {
	fs := flag.NewFlagSet("omnia cloud bootstrap-admin", flag.ContinueOnError)
	username := fs.String("username", "", "username for the first admin account")
	password := fs.String("password", "", "password for the first admin (prompted if empty)")
	email := fs.String("email", "", "optional email for the first admin account")
	issueToken := fs.Bool("issue-token", false, "also issue a managed token for the new admin (printed once)")

	if err := fs.Parse(os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	if strings.TrimSpace(*username) == "" {
		fmt.Fprintln(os.Stderr, "usage: omnia cloud bootstrap-admin --username <u> [--password <p>] [--email <e>] [--issue-token]")
		fmt.Fprintln(os.Stderr, "error: --username is required")
		exitFunc(1)
		return
	}

	pw := *password
	if pw == "" {
		var err error
		pw, err = readPasswordFn("Password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading password: %v\n", err)
			exitFunc(1)
			return
		}
	}

	// --issue-token only makes sense when managed tokens are actually enabled on the
	// server, and the issued token is only useful if it hashes with the SAME pepper
	// the running server validates against. Enforce the same boot gate here so an
	// operator doesn't mint a token the server will reject.
	if *issueToken {
		if !envBool("OMNIA_CLOUD_MANAGED_TOKENS") {
			fmt.Fprintln(os.Stderr, "error: --issue-token requires managed tokens enabled: set OMNIA_CLOUD_MANAGED_TOKENS=1")
			exitFunc(1)
			return
		}
		if cloud.IsDefaultTokenPepper(envx.Get("OMNIA_CLOUD_TOKEN_PEPPER")) {
			fmt.Fprintln(os.Stderr, "error: --issue-token requires a non-default OMNIA_CLOUD_TOKEN_PEPPER; refusing empty or development-default pepper")
			exitFunc(1)
			return
		}
	}

	cs, signup, issuer, closer, err := newBootstrapDeps(cloud.ConfigFromEnv(), *issueToken)
	if err != nil {
		fatal(err)
		return
	}
	if closer != nil {
		defer closer()
	}

	res, err := runBootstrapAdmin(context.Background(), cs, signup, issuer, bootstrapAdminOptions{
		Username:   strings.TrimSpace(*username),
		Email:      strings.TrimSpace(*email),
		Password:   pw,
		IssueToken: *issueToken,
	})
	if err != nil {
		switch {
		case errors.Is(err, errBootstrapAdminExists):
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		case errors.Is(err, auth.ErrUsernameRequired), errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrAccountExists):
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		default:
			fmt.Fprintf(os.Stderr, "bootstrap-admin failed: %v\n", err)
		}
		exitFunc(1)
		return
	}

	fmt.Printf("Created first admin account: id=%s username=%s\n", res.UserID, res.Username)
	if res.RawToken != "" {
		fmt.Println("Managed token (shown once — store it now, it cannot be retrieved again):")
		fmt.Printf("  %s\n", res.RawToken)
		fmt.Printf("  token id: %s\n", res.TokenID)
	}
}
