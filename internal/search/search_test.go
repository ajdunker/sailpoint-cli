// Copyright (c) 2021, SailPoint Technologies, Inc. All rights reserved.

package search

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	sailpointsdk "github.com/sailpoint-oss/golang-sdk/v3/search"
)

func TestBuildSearchStillFillsTheQueryResultFilter(t *testing.T) {
	searchObj, err := BuildSearch("name:a*", []string{"name"}, []string{"identities"})
	if err != nil {
		t.Fatalf("BuildSearch returned an error: %v", err)
	}
	if searchObj.Query.GetQuery() != "name:a*" {
		t.Errorf("expected the query to be kept, got %q", searchObj.Query.GetQuery())
	}
	if len(searchObj.Indices) != 1 || searchObj.Indices[0] != sailpointsdk.INDEX_IDENTITIES {
		t.Errorf("expected the identities index, got %v", searchObj.Indices)
	}
	if searchObj.QueryResultFilter == nil || len(searchObj.QueryResultFilter.GetIncludes()) != 0 {
		t.Errorf("expected an empty query result filter, got %v", searchObj.QueryResultFilter)
	}
}

func TestBuildSearchRejectsUnknownIndex(t *testing.T) {
	if _, err := BuildSearch("name:a*", nil, []string{"nonsense"}); err == nil {
		t.Fatal("expected an error for an unknown index")
	}
}

func TestApplyIncludesSetsQueryResultFilter(t *testing.T) {
	searchObj, err := BuildSearch("name:a*", nil, []string{"identities"})
	if err != nil {
		t.Fatalf("BuildSearch returned an error: %v", err)
	}

	ApplyIncludes(&searchObj, []string{"id", "name", "attributes.uid"})

	if searchObj.QueryResultFilter == nil {
		t.Fatal("expected a query result filter")
	}
	got := searchObj.QueryResultFilter.GetIncludes()
	if len(got) != 3 || got[0] != "id" || got[1] != "name" || got[2] != "attributes.uid" {
		t.Errorf("expected the listed fields, got %v", got)
	}
}

func TestApplyIncludesIsANoOpWhenEmpty(t *testing.T) {
	searchObj, err := BuildSearch("name:a*", nil, []string{"identities"})
	if err != nil {
		t.Fatalf("BuildSearch returned an error: %v", err)
	}

	ApplyIncludes(&searchObj, nil)

	if searchObj.QueryResultFilter != nil && len(searchObj.QueryResultFilter.GetIncludes()) != 0 {
		t.Errorf("expected no includes to be set, got %v", searchObj.QueryResultFilter.GetIncludes())
	}
}

func TestApplySearchAfter(t *testing.T) {
	searchObj, err := BuildSearch("name:a*", nil, []string{"identities"})
	if err != nil {
		t.Fatalf("BuildSearch returned an error: %v", err)
	}

	ApplySearchAfter(&searchObj, []string{"John Doe", "2c91808375d8e80a0175e1f88a575221"})

	got := searchObj.GetSearchAfter()
	if len(got) != 2 || got[1] != "2c91808375d8e80a0175e1f88a575221" {
		t.Errorf("expected the search after cursor, got %v", got)
	}
}

func TestSortValuesReadsNestedFields(t *testing.T) {
	document := map[string]interface{}{
		"id":   "2c9280a473",
		"name": "John Doe",
		"attributes": map[string]interface{}{
			"uid": "jdoe",
		},
	}

	got := sortValues(document, []string{"name", "-id", "+attributes.uid"})

	want := []string{"John Doe", "2c9280a473", "jdoe"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expected %v, got %v", want, got)
	}
}

func TestSortValuesStopsOnMissingField(t *testing.T) {
	document := map[string]interface{}{"id": "2c9280a473"}

	got := sortValues(document, []string{"id", "attributes.uid"})

	if len(got) != 1 || got[0] != "2c9280a473" {
		t.Errorf("expected only the values found before the gap, got %v", got)
	}
}

func TestTotalCountFromResponse(t *testing.T) {
	response := &http.Response{Header: http.Header{"X-Total-Count": []string{" 42 "}}}
	if n, ok := totalCountFromResponse(response); !ok || n != 42 {
		t.Errorf("expected 42, got %d (%v)", n, ok)
	}

	if _, ok := totalCountFromResponse(&http.Response{Header: http.Header{}}); ok {
		t.Error("expected no total count when the header is missing")
	}

	if _, ok := totalCountFromResponse(&http.Response{Header: http.Header{"X-Total-Count": []string{"n/a"}}}); ok {
		t.Error("expected no total count for a non numeric header")
	}

	if _, ok := totalCountFromResponse(nil); ok {
		t.Error("expected no total count for a nil response")
	}
}

func TestNewSearchErrorExplains413(t *testing.T) {
	err := newSearchError(&http.Response{StatusCode: http.StatusRequestEntityTooLarge}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"413", "--count", "--includes", "--limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got %q", want, err.Error())
		}
	}
}

func TestNewSearchErrorKeepsOtherStatusCodes(t *testing.T) {
	err := newSearchError(&http.Response{StatusCode: 500, Status: "500 Internal Server Error"}, nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected the status code to be reported, got %v", err)
	}

	wrapped := newSearchError(nil, nil)
	if wrapped == nil || wrapped.Error() != "search failed" {
		t.Errorf("expected a generic failure, got %v", wrapped)
	}
}

func TestBuildResultFileNameKeepsShortNames(t *testing.T) {
	got := BuildResultFileName("name:a*", "Identities")
	if got != "query=name:a*&indices=Identities" {
		t.Errorf("expected the plain name, got %q", got)
	}
}

func TestBuildResultFileNameTruncatesLongNames(t *testing.T) {
	long := strings.Repeat("a", 300)

	got := BuildResultFileName(long, "Identities")

	if len(got) > maxAutoFileNameLength {
		t.Errorf("expected a name of at most %d characters, got %d", maxAutoFileNameLength, len(got))
	}
	if !strings.HasPrefix(got, "query=") {
		t.Errorf("expected the name to keep the query prefix, got %q", got)
	}

	// The hash keeps long queries that share a prefix distinguishable.
	other := BuildResultFileName(long+"b", "Identities")
	if got == other {
		t.Errorf("expected different names for different queries, both were %q", got)
	}
}

func TestResultGroupsIsStableAndSkipsEmptyIndexes(t *testing.T) {
	results := SearchResults{Identities: []Identity{{ID: "1"}}}

	groups := resultGroups(results)

	if len(groups) != 1 || groups[0].index != "Identities" {
		t.Fatalf("expected only the identities group, got %v", groups)
	}

	if len(resultGroups(SearchResults{})) != 0 {
		t.Error("expected no groups for an empty result set")
	}
}

func TestWriteResultsPrintsAnArrayForASingleIndex(t *testing.T) {
	results := SearchResults{Identities: []Identity{{ID: "2c9280a473", Name: "John Doe"}}}

	out := runWriteResults(t, results, OutputOptions{ToStdout: true})

	var decoded []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("expected an array on stdout, got %q (%v)", out, err)
	}
	if len(decoded) != 1 || decoded[0]["id"] != "2c9280a473" {
		t.Errorf("unexpected payload: %s", out)
	}
}

func TestWriteResultsPrintsAnObjectForSeveralIndexes(t *testing.T) {
	results := SearchResults{
		Identities: []Identity{{ID: "2c9280a473"}},
		Roles:      []Role{{ID: "2c9280a474"}},
	}

	out := runWriteResults(t, results, OutputOptions{ToStdout: true})

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("expected an object on stdout, got %q (%v)", out, err)
	}
	if _, ok := decoded["identities"]; !ok {
		t.Errorf("expected an identities key, got %s", out)
	}
	if _, ok := decoded["roles"]; !ok {
		t.Errorf("expected a roles key, got %s", out)
	}
}

func TestWriteResultsPrintsAnEmptyArrayForNoMatches(t *testing.T) {
	out := runWriteResults(t, SearchResults{}, OutputOptions{ToStdout: true})

	if strings.TrimSpace(out) != "[]" {
		t.Errorf("expected an empty array, got %q", out)
	}
}

func TestWriteResultsWritesAnEmptyFileForNoMatches(t *testing.T) {
	folder := t.TempDir()

	if err := WriteResults(SearchResults{}, "name:zz*", folder, OutputOptions{}); err != nil {
		t.Fatalf("WriteResults returned an error: %v", err)
	}

	data, err := readFile(folder, "querynamezzindicesResults.json")
	if err != nil {
		t.Fatalf("expected a result file: %v", err)
	}
	if strings.TrimSpace(data) != "[]" {
		t.Errorf("expected an empty array in the file, got %q", data)
	}
}

func TestWriteResultsHonoursTheChosenFileName(t *testing.T) {
	folder := t.TempDir()
	results := SearchResults{Identities: []Identity{{ID: "2c9280a473"}}}

	if err := WriteResults(results, "name:a*", folder, OutputOptions{FileName: "active-identities"}); err != nil {
		t.Fatalf("WriteResults returned an error: %v", err)
	}

	data, err := readFile(folder, "active-identities.json")
	if err != nil {
		t.Fatalf("expected the chosen file name to be used: %v", err)
	}
	if !strings.Contains(data, "2c9280a473") {
		t.Errorf("unexpected payload: %s", data)
	}
}

func TestWriteResultsSplitsSeveralIndexes(t *testing.T) {
	folder := t.TempDir()
	results := SearchResults{
		Identities: []Identity{{ID: "2c9280a473"}},
		Roles:      []Role{{ID: "2c9280a474"}},
	}

	if err := WriteResults(results, "name:a*", folder, OutputOptions{FileName: "matches"}); err != nil {
		t.Fatalf("WriteResults returned an error: %v", err)
	}

	for _, name := range []string{"matches-Identities.json", "matches-Roles.json"} {
		if _, err := readFile(folder, name); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
}

func TestWriteResultsTruncatesDerivedFileNames(t *testing.T) {
	folder := t.TempDir()
	long := strings.Repeat("a", 400)
	results := SearchResults{Identities: []Identity{{ID: "2c9280a473"}}}

	if err := WriteResults(results, long, folder, OutputOptions{}); err != nil {
		t.Fatalf("WriteResults returned an error: %v", err)
	}

	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatalf("unable to read the result folder: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected a single result file, got %d", len(entries))
	}
	if len(entries[0].Name()) > maxAutoFileNameLength+5 {
		t.Errorf("expected a truncated file name, got %q", entries[0].Name())
	}
}

// runWriteResults writes the results to a buffer and returns the JSON it produced.
func runWriteResults(t *testing.T, results SearchResults, opts OutputOptions) string {
	t.Helper()

	buffer := new(bytes.Buffer)
	opts.Writer = buffer

	if err := WriteResults(results, "name:a*", "search_results", opts); err != nil {
		t.Fatalf("WriteResults returned an error: %v", err)
	}

	return buffer.String()
}

func readFile(folder string, name string) (string, error) {
	data, err := os.ReadFile(folder + string(os.PathSeparator) + name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
