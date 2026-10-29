// Command publish-please automates releases from Conventional Commits on
// Gitea, Forgejo, GitHub and GitLab.
package main

import (
	"os"

	"github.com/MdTanvirHossainTusher/publish-please/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
