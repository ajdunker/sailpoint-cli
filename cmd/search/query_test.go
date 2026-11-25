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

func TestValidateQueryFlagsRejectsNegativeLimitAndOffset(t *testing.T) {
	cmd := newQueryCmd()
	if err := cmd.Flags().Set("limit", "-1"); err != nil {
		t.Fatalf("unable to set --limit: %v", err)
	}
	if err := validateQueryFlags(cmd, false, false, -1, 0); err == nil {
		t.Error("expected an error for negative --limit")
	}

	cmd = newQueryCmd()
	if err := cmd.Flags().Set("offset", "-5"); err != nil {
		t.Fatalf("unable to set --offset: %v", err)
	}
	if err := validateQueryFlags(cmd, false, false, 0, -5); err == nil {
		t.Error("expected an error for negative --offset")
	}
}

func TestValidateQueryFlagsRejectsConflictingCombinations(t *testing.T) {
	t.Run("page-all with offset", func(t *testing.T) {
		cmd := newQueryCmd()
		if err := cmd.Flags().Set("page-all", "true"); err != nil {
			t.Fatalf("unable to set --page-all: %v", err)
		}
		if err := cmd.Flags().Set("offset", "10"); err != nil {
			t.Fatalf("unable to set --offset: %v", err)
		}
		if err := validateQueryFlags(cmd, false, true, 0, 10); err == nil {
			t.Error("expected an error when combining --page-all with --offset")
		}
	})

	tests := []struct {
		name  string
		flag  string
		value string
	}{
		{name: "count with page-all", flag: "page-all", value: "true"},
		{name: "count with limit", flag: "limit", value: "10"},
		{name: "count with offset", flag: "offset", value: "5"},
		{name: "count with file-name", flag: "file-name", value: "out"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newQueryCmd()
			if err := cmd.Flags().Set("count", "true"); err != nil {
				t.Fatalf("unable to set --count: %v", err)
			}
			if err := cmd.Flags().Set(tt.flag, tt.value); err != nil {
				t.Fatalf("unable to set --%s: %v", tt.flag, err)
			}
			if err := validateQueryFlags(cmd, true, tt.flag == "page-all", 10, 5); err == nil {
				t.Errorf("expected an error when combining --count with --%s", tt.flag)
			}
		})
	}

	t.Run("count with stdout is allowed", func(t *testing.T) {
		cmd := newQueryCmd()
		if err := cmd.Flags().Set("count", "true"); err != nil {
			t.Fatalf("unable to set --count: %v", err)
		}
		if err := cmd.Flags().Set("stdout", "true"); err != nil {
			t.Fatalf("unable to set --stdout: %v", err)
		}
		if err := validateQueryFlags(cmd, true, false, 0, 0); err != nil {
			t.Errorf("expected --count --stdout to be allowed, got %v", err)
		}
	})
}
