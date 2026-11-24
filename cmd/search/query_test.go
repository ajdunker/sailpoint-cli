// Copyright (c) 2021, SailPoint Technologies, Inc. All rights reserved.

package search

import (
	"testing"
)

func TestQueryCommandRegistersTheExtendedFlags(t *testing.T) {
	cmd := newQueryCmd()

	want := map[string]string{
		"count":    "false",
		"page-all": "false",
		"stdout":   "false",
		"limit":    "0",
		"offset":   "0",
		"includes": "[]",
	}

	for name, def := range want {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("expected the query command to expose --%s", name)
			continue
		}
		if flag.DefValue != def {
			t.Errorf("expected --%s to default to %s, got %s", name, def, flag.DefValue)
		}
	}

	if cmd.Flags().Lookup("file-name") == nil {
		t.Error("expected the query command to expose --file-name")
	}
}

func TestQueryCommandKeepsTheExistingFlags(t *testing.T) {
	cmd := newQueryCmd()

	for _, name := range []string{"folderPath", "indices", "sort"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected the query command to keep --%s", name)
		}
	}

	if usesExtendedFlags(cmd) {
		t.Fatal("expected no flags to be marked as changed on a fresh command")
	}
}

func TestUsesExtendedFlagsOnlyReportsSetFlags(t *testing.T) {
	cmd := newQueryCmd()

	if err := cmd.Flags().Set("count", "true"); err != nil {
		t.Fatalf("unable to set --count: %v", err)
	}
	if !usesExtendedFlags(cmd) {
		t.Error("expected --count to select the extended code path")
	}

	other := newQueryCmd()
	if usesExtendedFlags(other) {
		t.Error("expected the default code path when no flag is set")
	}
}
