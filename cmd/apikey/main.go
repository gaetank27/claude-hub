// Command apikey manages hub API keys directly in the database, for bootstrapping
// a machine client without going through the web UI. Safe against the live DB
// (SQLite WAL + busy_timeout).
//
// Usage:
//
//	apikey create -name <label> [-project <project id or slug>]
//	apikey list
//	apikey revoke <key id>
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"system/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	dataDir := os.Getenv("SYSTEM_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(os.Getenv("HOME"), ".local", "share", "system")
	}
	db, err := store.Open(filepath.Join(dataDir, "system.db"))
	must(err)

	switch os.Args[1] {
	case "create":
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		name := fs.String("name", "", "label for the key")
		project := fs.String("project", "", "restrict the key to this project (id or slug)")
		must(fs.Parse(os.Args[2:]))
		if *name == "" {
			usage()
		}
		projectID := ""
		if *project != "" {
			p, err := db.GetProject(*project)
			if err != nil {
				p, err = db.GetProjectBySlug(*project)
			}
			must(err)
			projectID = p.ID
		}
		k, secret, err := db.CreateAPIKey(*name, projectID)
		must(err)
		fmt.Fprintf(os.Stderr, "created key %s (%s), project=%q\n", k.ID, k.Name, k.ProjectID)
		fmt.Println(secret)
	case "list":
		keys, err := db.ListAPIKeys()
		must(err)
		for _, k := range keys {
			used := "never"
			if k.LastUsedAt != nil {
				used = k.LastUsedAt.Format("2006-01-02 15:04")
			}
			fmt.Printf("%s  %-24s project=%-36q created=%s last-used=%s\n",
				k.ID, k.Name, k.ProjectID, k.CreatedAt.Format("2006-01-02"), used)
		}
	case "revoke":
		if len(os.Args) < 3 {
			usage()
		}
		must(db.DeleteAPIKey(os.Args[2]))
		fmt.Println("revoked")
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: apikey create -name <label> [-project <id|slug>] | list | revoke <id>")
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
