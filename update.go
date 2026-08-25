package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mariobgsp/routre/internal/update"
)

// cmdUpdate implements `routre update`: check for (and optionally apply)
// the latest GitHub release over this binary. Zero dependencies; never calls
// api.github.com.
func cmdUpdate(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "only report whether an update is available")
	if err := fs.Parse(args); err != nil {
		return err
	}

	current := strings.TrimSpace(version)
	if current == "" || current == "dev" {
		logger.Printf("current build has no embedded version (%q) — cannot tell if this is newer", version)
	} else {
		logger.Printf("current version: %s", current)
	}

	rel, err := update.Latest()
	if err != nil {
		return fmt.Errorf("checking for updates: %w", err)
	}
	logger.Printf("latest release: %s", rel.Tag)

	switch update.CompareVersions(current, rel.Tag) {
	case 1:
		fmt.Printf("routre %s is newer than %s — nothing to do\n", current, rel.Tag)
		return nil
	case 0:
		fmt.Println("already up to date:", current)
		return nil
	}
	if *checkOnly {
		fmt.Printf("update available: %s -> %s\n", current, rel.Tag)
		return nil
	}
	if current == "" || current == "dev" {
		fmt.Println("(installing anyway — use routre version afterwards to confirm)")
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve own binary: %w", err)
	}
	if reason := npmManaged(exe); reason != "" {
		return fmt.Errorf(
			"this copy was installed by npm (%s).\n"+
				"npm distribution is deprecated — switch to the direct install:\n"+
				"  npm uninstall -g routre\n"+
				"  curl -fsSL https://raw.githubusercontent.com/%s/%s/main/install.sh | sh",
			reason, update.Owner, update.Repo,
		)
	}

	fmt.Printf("updating %s -> %s ...\n", current, rel.Tag)
	if err := rel.Apply(exe); err != nil {
		return err
	}
	fmt.Println("updated to", rel.Tag, "— run 'routre version' to confirm")
	return nil
}

// npmManaged reports why (and only why) exe looks like an npm-managed copy:
// global installs live under .../node_modules/... and typically under an
// npm prefix dir (~/.npm-global, /usr/local/lib/node_modules, nvm dirs).
func npmManaged(exe string) string {
	parts := strings.Split(filepath.ToSlash(exe), "/")
	for _, p := range parts {
		switch p {
		case "node_modules", ".npm-global":
			return p + " path"
		}
	}
	if strings.Contains(exe, "/.nvm/") {
		return ".nvm path"
	}
	return ""
}
