// Command restorefilter narrows a portable Omnia export down to a chosen set of
// observation sync_ids and re-emits it as a valid export.
//
// It exists because a portable export carries a checksum computed by
// store.ExportData's own MarshalJSON, so a hand-edited subset is rejected at
// import time. Round-tripping through the real type is the only way to produce
// a subset the importer accepts.
//
// Usage: restorefilter <full-export.json> <out.json> <sync_id>...
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/velion/omnia/internal/store"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: restorefilter <full-export.json> <out.json> <sync_id>...")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}
	var data store.ExportData
	if err := json.Unmarshal(raw, &data); err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}

	keep := map[string]bool{}
	for _, id := range os.Args[3:] {
		keep[id] = true
	}

	// A hard-deleted observation leaves a tombstone, and the importer refuses
	// to resurrect a tombstoned sync_id — a deliberate guard so a deletion
	// cannot come back through a sync. Restoring content therefore requires a
	// NEW identity: OMNIA_RESTORE_SUFFIX rewrites each sync_id, keeping title,
	// content, type, project and timestamps intact.
	suffix := os.Getenv("OMNIA_RESTORE_SUFFIX")

	var obs []store.Observation
	sessions := map[string]bool{}
	for _, o := range data.Observations {
		if keep[o.SyncID] {
			sessions[o.SessionID] = true
			if suffix != "" {
				o.SyncID = o.SyncID[:len(o.SyncID)-len(suffix)] + suffix
			}
			obs = append(obs, o)
		}
	}
	var sess []store.Session
	for _, s := range data.Sessions {
		if sessions[s.ID] {
			sess = append(sess, s)
		}
	}

	out := store.ExportData{
		SchemaVersion: data.SchemaVersion,
		Version:       data.Version,
		ExportedAt:    data.ExportedAt,
		Sessions:      sess,
		Observations:  obs,
		Prompts:       []store.Prompt{},
		Relations:     []store.PortableRow{},
		Anchors:       []store.PortableRow{},
		Procedures:    []store.PortableRow{},
	}
	out.Counts = store.ExportCounts{
		Sessions: len(sess), Observations: len(obs),
	}

	encoded, err := json.MarshalIndent(&out, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], encoded, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Printf("escrito %s: %d sesión(es), %d observación(es)\n", os.Args[2], len(sess), len(obs))
}
