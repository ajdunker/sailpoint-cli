// Copyright (c) 2021, SailPoint Technologies, Inc. All rights reserved.

package search

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	sailpoint "github.com/sailpoint-oss/golang-sdk/v3"
	sailpointsdk "github.com/sailpoint-oss/golang-sdk/v3/search"
)

func newTestAPIClient(t *testing.T, baseURL string) sailpoint.APIClient {
	t.Helper()
	cfg := sailpoint.NewCLIConfiguration(sailpoint.ClientConfiguration{
		Token:   "test-token",
		BaseURL: baseURL,
	})
	client := sailpoint.NewAPIClient(cfg)
	// Disable retries in tests so failure paths execute once and fail fast.
	if cfg.HTTPClient != nil {
		cfg.HTTPClient.RetryMax = 0
	}
	return *client
}

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

func TestApplyIncludesSetsQueryResultFilterAndTrimsWhitespace(t *testing.T) {
	searchObj, err := BuildSearch("name:a*", nil, []string{"identities"})
	if err != nil {
		t.Fatalf("BuildSearch returned an error: %v", err)
	}

	ApplyIncludes(&searchObj, []string{" id ", " name", "", "  attributes.uid  "})

	if searchObj.QueryResultFilter == nil {
		t.Fatal("expected a query result filter")
	}
	got := searchObj.QueryResultFilter.GetIncludes()
	if len(got) != 3 || got[0] != "id" || got[1] != "name" || got[2] != "attributes.uid" {
		t.Errorf("expected the trimmed fields, got %v", got)
	}
}

func TestApplyIncludesIsANoOpWhenEmpty(t *testing.T) {
	searchObj, err := BuildSearch("name:a*", nil, []string{"identities"})
	if err != nil {
		t.Fatalf("BuildSearch returned an error: %v", err)
	}

	ApplyIncludes(&searchObj, []string{"", "   "})

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

	got, ok := sortValues(document, []string{"name", "-id", "+attributes.uid"})
	if !ok {
		t.Fatalf("expected ok=true, got false (values=%v)", got)
	}

	want := []string{"John Doe", "2c9280a473", "jdoe"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expected %v, got %v", want, got)
	}
}

func TestSortValuesRejectsPartialCursorOnMissingField(t *testing.T) {
	document := map[string]interface{}{"id": "2c9280a473"}

	got, ok := sortValues(document, []string{"id", "attributes.uid"})
	if ok || got != nil {
		t.Errorf("expected (nil, false) when a sort field is missing, got (%v, %v)", got, ok)
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

func TestBuildResultFileNameTruncatesLongNamesAndPreservesUTF8(t *testing.T) {
	long := strings.Repeat("é", 120)

	got := BuildResultFileName(long, "Identities")

	if len(got) > maxAutoFileNameLength {
		t.Errorf("expected a name of at most %d bytes, got %d", maxAutoFileNameLength, len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("expected valid UTF-8 after truncation, got %q", got)
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
	results := SearchResults{
		Identities: []Identity{{ID: "1"}},
		Roles:      []Role{}, // non-nil empty slice must be skipped
	}

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

func TestWriteResultsHonoursTheChosenFileNameAndStripsJSONSuffix(t *testing.T) {
	folder := t.TempDir()
	results := SearchResults{Identities: []Identity{{ID: "2c9280a473"}}}

	if err := WriteResults(results, "name:a*", folder, OutputOptions{FileName: "active-identities.json"}); err != nil {
		t.Fatalf("WriteResults returned an error: %v", err)
	}

	data, err := readFile(folder, "active-identities.json")
	if err != nil {
		t.Fatalf("expected active-identities.json to be created: %v", err)
	}
	if !strings.Contains(data, "2c9280a473") {
		t.Errorf("unexpected payload: %s", data)
	}
}

func TestWriteResultsOverwritesLongerFileWithoutTrailingBytes(t *testing.T) {
	folder := t.TempDir()
	big := SearchResults{Identities: []Identity{{ID: "2c9280a473", Name: "Long Identity Payload"}}}

	if err := WriteResults(big, "name:a*", folder, OutputOptions{FileName: "report"}); err != nil {
		t.Fatalf("first WriteResults returned an error: %v", err)
	}
	if err := WriteResults(SearchResults{}, "name:zz*", folder, OutputOptions{FileName: "report"}); err != nil {
		t.Fatalf("second WriteResults returned an error: %v", err)
	}

	data, err := readFile(folder, "report.json")
	if err != nil {
		t.Fatalf("unable to read report.json: %v", err)
	}
	var parsed []interface{}
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		t.Fatalf("expected valid JSON after overwrite with shorter payload, got %q (%v)", data, err)
	}
	if len(parsed) != 0 {
		t.Errorf("expected empty array, got %v", parsed)
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

func TestWriteRawResultsPreservesCustomAttributesWithoutZeroBloat(t *testing.T) {
	entries := []map[string]interface{}{
		{
			"id":   "id-1",
			"name": "Alice",
			"attributes": map[string]interface{}{
				"cloudLifecycleState": "active",
				"department":          "Engineering",
			},
		},
	}

	buf := new(bytes.Buffer)
	if err := WriteRawResults(entries, "attributes.cloudLifecycleState:active", t.TempDir(), OutputOptions{
		ToStdout: true,
		Writer:   buf,
	}); err != nil {
		t.Fatalf("WriteRawResults returned an error: %v", err)
	}

	var decoded []map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("expected JSON array, got %q (%v)", buf.String(), err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 document, got %d", len(decoded))
	}
	if _, hasAccounts := decoded[0]["accounts"]; hasAccounts {
		t.Errorf("expected omitted struct fields not to be zero-filled, got %s", buf.String())
	}
	attrs, ok := decoded[0]["attributes"].(map[string]interface{})
	if !ok || attrs["cloudLifecycleState"] != "active" || attrs["department"] != "Engineering" {
		t.Errorf("expected custom attributes to be preserved, got %s", buf.String())
	}
}

func TestWriteRawResultsGroupsMultipleIndices(t *testing.T) {
	entries := []map[string]interface{}{
		{"_type": "identity", "id": "id-1"},
		{"_type": "role", "id": "role-1"},
	}

	buf := new(bytes.Buffer)
	if err := WriteRawResults(entries, "name:a*", t.TempDir(), OutputOptions{
		ToStdout: true,
		Writer:   buf,
	}); err != nil {
		t.Fatalf("WriteRawResults returned an error: %v", err)
	}

	var decoded map[string][]map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("expected grouped JSON object, got %q (%v)", buf.String(), err)
	}
	if len(decoded["identities"]) != 1 || len(decoded["roles"]) != 1 {
		t.Errorf("unexpected grouped payload: %s", buf.String())
	}
}

func TestPerformSearchReturns413Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"detailCode":"413"}`))
	}))
	defer server.Close()

	client := newTestAPIClient(t, server.URL)
	searchObj, _ := BuildSearch("name:a*", nil, []string{"identities"})

	_, err := PerformSearch(client, searchObj)
	if err == nil {
		t.Fatal("expected PerformSearch to return an error on 413")
	}
	if !strings.Contains(err.Error(), "413") || !strings.Contains(err.Error(), "--includes") {
		t.Errorf("expected actionable 413 message, got %v", err)
	}
}

func TestPerformCountUsesCountEndpointAndFallsBackWhenHeaderMissing(t *testing.T) {
	t.Run("count endpoint returns header", func(t *testing.T) {
		var searchCalls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/search/v1/count") {
				w.Header().Set("X-Total-Count", "1234")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			atomic.AddInt32(&searchCalls, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client := newTestAPIClient(t, server.URL)
		searchObj, _ := BuildSearch("name:a*", nil, []string{"identities"})

		n, err := PerformCount(client, searchObj)
		if err != nil {
			t.Fatalf("PerformCount error = %v", err)
		}
		if n != 1234 {
			t.Errorf("got %d, want 1234", n)
		}
		if atomic.LoadInt32(&searchCalls) != 0 {
			t.Errorf("expected fallback not to be called")
		}
	})

	t.Run("falls back to search when count header is missing", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/search/v1/count") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("count") != "true" {
				t.Errorf("unexpected fallback query params: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Total-Count", "77")
			_, _ = w.Write([]byte(`[]`))
		}))
		defer server.Close()

		client := newTestAPIClient(t, server.URL)
		searchObj, _ := BuildSearch("name:a*", nil, []string{"identities"})

		n, err := PerformCount(client, searchObj)
		if err != nil {
			t.Fatalf("PerformCount error = %v", err)
		}
		if n != 77 {
			t.Errorf("got %d, want 77", n)
		}
	})

	t.Run("does not retry on transport error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := server.URL
		server.Close()

		client := newTestAPIClient(t, url)
		searchObj, _ := BuildSearch("name:a*", nil, []string{"identities"})

		if _, err := PerformCount(client, searchObj); err == nil {
			t.Fatal("expected error when server is unreachable")
		}
	})
}

func TestPerformSearchPageHonoursLimitOffsetAndCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("offset") != "4" || r.URL.Query().Get("count") != "true" {
			t.Errorf("unexpected query params: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", "99")
		_, _ = w.Write([]byte(`[{"_type":"identity","id":"id-5","name":"Eva"}]`))
	}))
	defer server.Close()

	client := newTestAPIClient(t, server.URL)
	searchObj, _ := BuildSearch("name:*", nil, []string{"identities"})

	res, total, err := PerformSearchPage(client, searchObj, 2, 4)
	if err != nil {
		t.Fatalf("PerformSearchPage error = %v", err)
	}
	if total != 99 {
		t.Errorf("got total %d, want 99", total)
	}
	if len(res.Identities) != 1 || res.Identities[0].ID != "id-5" {
		t.Errorf("unexpected identities: %+v", res.Identities)
	}
}

func TestPerformRawSearchAllPagesUsesCustomLimitTieBreakerAndIncludesSortFields(t *testing.T) {
	var page int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		if r.URL.Query().Get("limit") != "2" {
			t.Errorf("page %d: expected limit=2, got %s", page, r.URL.Query().Get("limit"))
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// Verify id tie-breaker was appended to sort and sort fields were added to includes.
		sortSlice, _ := body["sort"].([]interface{})
		if len(sortSlice) != 2 || sortSlice[0] != "name" || sortSlice[1] != "id" {
			t.Errorf("page %d: expected sort=[name id], got %v", page, sortSlice)
		}
		qrf, _ := body["queryResultFilter"].(map[string]interface{})
		incSlice, _ := qrf["includes"].([]interface{})
		incJoined := ""
		for _, v := range incSlice {
			incJoined += v.(string) + ","
		}
		if !strings.Contains(incJoined, "name,") || !strings.Contains(incJoined, "id,") {
			t.Errorf("page %d: expected sort fields in includes, got %v", page, incSlice)
		}

		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			w.Header().Set("X-Total-Count", "3")
			_, _ = w.Write([]byte(`[{"_type":"identity","id":"1","name":"Alice"},{"_type":"identity","id":"2","name":"Bob"}]`))
			return
		}
		after, _ := body["searchAfter"].([]interface{})
		if len(after) != 2 || after[0] != "Bob" || after[1] != "2" {
			t.Errorf("page 2: expected searchAfter=[Bob 2], got %v", after)
		}
		_, _ = w.Write([]byte(`[{"_type":"identity","id":"3","name":"Cara"}]`))
	}))
	defer server.Close()

	client := newTestAPIClient(t, server.URL)
	searchObj, _ := BuildSearch("name:*", []string{"name"}, []string{"identities"})
	ApplyIncludes(&searchObj, []string{"name"})

	entries, total, err := PerformRawSearchAllPages(client, searchObj, 2)
	if err != nil {
		t.Fatalf("PerformRawSearchAllPages error = %v", err)
	}
	if total != 3 {
		t.Errorf("got total %d, want 3", total)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
}

func TestPerformRawSearchAllPagesErrorsWhenSortFieldMissingOrCursorStuck(t *testing.T) {
	t.Run("missing sort field on full page", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"_type":"identity","name":"NoID"}]`))
		}))
		defer server.Close()

		client := newTestAPIClient(t, server.URL)
		searchObj, _ := BuildSearch("name:*", nil, []string{"identities"})

		_, _, err := PerformRawSearchAllPages(client, searchObj, 1)
		if err == nil || !strings.Contains(err.Error(), "missing one or more sort fields") {
			t.Fatalf("expected missing sort field error, got %v", err)
		}
	})

	t.Run("repeated cursor breaks loop with error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"_type":"identity","id":"same-id"}]`))
		}))
		defer server.Close()

		client := newTestAPIClient(t, server.URL)
		searchObj, _ := BuildSearch("name:*", nil, []string{"identities"})

		_, _, err := PerformRawSearchAllPages(client, searchObj, 1)
		if err == nil || !strings.Contains(err.Error(), "cursor did not advance") {
			t.Fatalf("expected stuck cursor error, got %v", err)
		}
	})
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
