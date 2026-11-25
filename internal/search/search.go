package search

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/log"
	"github.com/mitchellh/mapstructure"
	sailpoint "github.com/sailpoint-oss/golang-sdk/v3"
	sailpointsdk "github.com/sailpoint-oss/golang-sdk/v3/search"
	"github.com/sailpoint-oss/sailpoint-cli/internal/output"
)

// DefaultPageSize is the number of records requested for each page of a search.
const DefaultPageSize int32 = 250

// maxAutoFileNameLength bounds the length of an auto generated result file name so
// that it always fits within the per component limit of common filesystems.
const maxAutoFileNameLength = 100

func ParseIndices(indices string) (sailpointsdk.Index, error) {
	switch indices {
	case "accessprofiles":
		return sailpointsdk.INDEX_ACCESSPROFILES, nil
	case "accountactivities":
		return sailpointsdk.INDEX_ACCOUNTACTIVITIES, nil
	case "entitlements":
		return sailpointsdk.INDEX_ENTITLEMENTS, nil
	case "events":
		return sailpointsdk.INDEX_EVENTS, nil
	case "identities":
		return sailpointsdk.INDEX_IDENTITIES, nil
	case "roles":
		return sailpointsdk.INDEX_ROLES, nil
	default:
		return "*", fmt.Errorf("index provided is invalid")
	}
}

func BuildSearch(searchQuery string, sort []string, indices []string) (sailpointsdk.Search, error) {

	search := sailpointsdk.NewSearch()
	search.Query = sailpointsdk.NewQuery()
	search.QueryResultFilter = sailpointsdk.NewQueryresultfilter()
	search.Query.Query = &searchQuery
	search.Sort = sort
	search.Indices = []sailpointsdk.Index{}

	for i := 0; i < len(indices); i++ {
		tempIndices, err := ParseIndices(indices[i])

		if err != nil {
			return *search, err
		}

		search.Indices = append(search.Indices, tempIndices)
	}

	return *search, nil
}

// ApplyIncludes limits the fields returned for every document by filling in
// queryResultFilter.includes. It trims whitespace, ignores empty entries, and is a
// no-op when no fields are provided so the server keeps returning whole documents.
func ApplyIncludes(search *sailpointsdk.Search, includes []string) {
	cleaned := make([]string, 0, len(includes))
	for _, field := range includes {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return
	}
	if search.QueryResultFilter == nil {
		search.QueryResultFilter = sailpointsdk.NewQueryresultfilter()
	}
	search.QueryResultFilter.SetIncludes(cleaned)
}

// ApplySearchAfter shifts the search window to start after the given sort values.
func ApplySearchAfter(search *sailpointsdk.Search, searchAfter []string) {
	if len(searchAfter) == 0 {
		return
	}
	search.SearchAfter = searchAfter
}

// ensureStableSort returns a sort slice suitable for searchAfter paging: it defaults to
// ["id"] when empty and appends "id" as a tie-breaker when no id sort is present.
func ensureStableSort(sort []string) []string {
	if len(sort) == 0 {
		return []string{"id"}
	}
	result := make([]string, len(sort))
	copy(result, sort)
	for _, entry := range result {
		if stripSortDirection(entry) == "id" {
			return result
		}
	}
	return append(result, "id")
}

// ensureSortFieldsIncluded adds any missing sort fields to queryResultFilter.includes so
// searchAfter paging can always extract the cursor from the last document on each page.
func ensureSortFieldsIncluded(search *sailpointsdk.Search) {
	if search.QueryResultFilter == nil || len(search.QueryResultFilter.Includes) == 0 {
		return
	}
	includes := make([]string, len(search.QueryResultFilter.Includes))
	copy(includes, search.QueryResultFilter.Includes)
	for _, entry := range search.Sort {
		field := stripSortDirection(entry)
		if field == "" || isFieldIncluded(includes, field) {
			continue
		}
		includes = append(includes, field)
	}
	clone := *search.QueryResultFilter
	clone.SetIncludes(includes)
	search.QueryResultFilter = &clone
}

func isFieldIncluded(includes []string, field string) bool {
	for _, inc := range includes {
		if inc == field || strings.HasPrefix(field, inc+".") {
			return true
		}
	}
	return false
}

func stripSortDirection(entry string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(entry), "+"), "-")
}

// decodeEntries converts the raw documents returned by the search endpoint into the
// typed per index slices of SearchResults.
func decodeEntries(entries []map[string]interface{}, results *SearchResults) error {
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		switch entry["_type"] {
		case "accountactivity":
			var AccountActivity AccountActivity
			err := mapstructure.Decode(entry, &AccountActivity)
			if err != nil {
				return err
			}
			results.AccountActivities = append(results.AccountActivities, AccountActivity)

		case "accessprofile":
			var AccessProfile AccessProfile
			err := mapstructure.Decode(entry, &AccessProfile)
			if err != nil {
				return err
			}
			results.AccessProfiles = append(results.AccessProfiles, AccessProfile)

		case "entitlement":
			var Entitlement Entitlement
			err := mapstructure.Decode(entry, &Entitlement)
			if err != nil {
				return err
			}
			results.Entitlements = append(results.Entitlements, Entitlement)

		case "event":
			var Event Event
			err := mapstructure.Decode(entry, &Event)
			if err != nil {
				return err
			}
			results.Events = append(results.Events, Event)

		case "identity":
			var Identity Identity
			err := mapstructure.Decode(entry, &Identity)
			if err != nil {
				return err
			}
			results.Identities = append(results.Identities, Identity)

		case "role":
			var Role Role
			err := mapstructure.Decode(entry, &Role)
			if err != nil {
				return err
			}
			results.Roles = append(results.Roles, Role)
		}
	}

	return nil
}

func fetchSearchDefault(apiClient sailpoint.APIClient, search sailpointsdk.Search) ([]map[string]interface{}, error) {
	ctx := context.TODO()
	resp, r, err := sailpoint.PaginateWithDefaults[map[string]interface{}](apiClient.SearchAPI.SearchPostV1(ctx).Search(search))
	if err != nil {
		return nil, newSearchError(r, err)
	}

	log.Debug("Search complete")
	return resp, nil
}

func PerformSearch(apiClient sailpoint.APIClient, search sailpointsdk.Search) (SearchResults, error) {
	var SearchResults SearchResults

	resp, err := fetchSearchDefault(apiClient, search)
	if err != nil {
		return SearchResults, err
	}

	if err := decodeEntries(resp, &SearchResults); err != nil {
		return SearchResults, err
	}

	return SearchResults, nil
}

// PerformRawSearch runs the default paginated search and returns the raw document maps.
func PerformRawSearch(apiClient sailpoint.APIClient, search sailpointsdk.Search) ([]map[string]interface{}, error) {
	return fetchSearchDefault(apiClient, search)
}

func PerformSearchWithLimit(apiClient sailpoint.APIClient, search sailpointsdk.Search, limit int32) (SearchResults, error) {
	var SearchResults SearchResults

	if limit == 0 {
		limit = 500
	}

	ctx := context.TODO()
	resp, r, err := apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(limit).Execute()
	if err != nil {
		return SearchResults, newSearchError(r, err)
	}

	log.Debug("Search complete")

	err = decodeEntries(resp, &SearchResults)
	if err != nil {
		return SearchResults, err
	}

	return SearchResults, nil
}

func fetchSearchPage(apiClient sailpoint.APIClient, search sailpointsdk.Search, limit int32, offset int32) ([]map[string]interface{}, int32, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}

	ctx := context.TODO()
	resp, r, err := apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(limit).Offset(offset).Count(true).Execute()
	if err != nil {
		return nil, -1, newSearchError(r, err)
	}

	log.Debug("Search complete", "limit", limit, "offset", offset)

	total := int32(-1)
	if r != nil {
		if n, ok := totalCountFromResponse(r); ok {
			total = n
		}
	}

	return resp, total, nil
}

// PerformSearchPage runs a single search request honouring limit and offset, and returns
// the decoded results together with the total match count reported by the server. The
// total is -1 when the server did not send an X-Total-Count header.
func PerformSearchPage(apiClient sailpoint.APIClient, search sailpointsdk.Search, limit int32, offset int32) (SearchResults, int32, error) {
	var SearchResults SearchResults

	resp, total, err := fetchSearchPage(apiClient, search, limit, offset)
	if err != nil {
		return SearchResults, total, err
	}

	if err := decodeEntries(resp, &SearchResults); err != nil {
		return SearchResults, total, err
	}

	return SearchResults, total, nil
}

// PerformRawSearchPage runs a single search request and returns the raw document maps.
func PerformRawSearchPage(apiClient sailpoint.APIClient, search sailpointsdk.Search, limit int32, offset int32) ([]map[string]interface{}, int32, error) {
	return fetchSearchPage(apiClient, search, limit, offset)
}

func fetchSearchAllPages(apiClient sailpoint.APIClient, search sailpointsdk.Search, pageSize int32) ([]map[string]interface{}, int32, error) {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}

	search.Sort = ensureStableSort(search.Sort)
	ensureSortFieldsIncluded(&search)

	var all []map[string]interface{}
	total := int32(-1)
	prevCursor := ""

	for {
		ctx := context.TODO()
		resp, r, err := apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(pageSize).Count(total < 0).Execute()
		if err != nil {
			return all, total, newSearchError(r, err)
		}

		if r != nil && total < 0 {
			if n, ok := totalCountFromResponse(r); ok {
				total = n
			}
		}

		all = append(all, resp...)

		log.Debug("Page complete", "records", len(resp), "searchAfter", search.SearchAfter)

		if len(resp) < int(pageSize) {
			break
		}

		last := resp[len(resp)-1]
		searchAfter, ok := sortValues(last, search.Sort)
		if !ok {
			return all, total, fmt.Errorf("cannot continue --page-all: last record on page is missing one or more sort fields (%s)", strings.Join(search.Sort, ", "))
		}

		cursorKey := strings.Join(searchAfter, "\x00")
		if prevCursor != "" && cursorKey == prevCursor {
			return all, total, fmt.Errorf("cannot continue --page-all: searchAfter cursor did not advance (%v)", searchAfter)
		}
		prevCursor = cursorKey
		ApplySearchAfter(&search, searchAfter)
	}

	return all, total, nil
}

// PerformSearchAllPages walks a result set larger than the server page limit using
// searchAfter paging with DefaultPageSize records per page.
func PerformSearchAllPages(apiClient sailpoint.APIClient, search sailpointsdk.Search) (SearchResults, int32, error) {
	return PerformSearchAllPagesWithLimit(apiClient, search, DefaultPageSize)
}

// PerformSearchAllPagesWithLimit walks a result set using searchAfter paging with the
// given page size (defaulting to DefaultPageSize when pageSize <= 0).
func PerformSearchAllPagesWithLimit(apiClient sailpoint.APIClient, search sailpointsdk.Search, pageSize int32) (SearchResults, int32, error) {
	var SearchResults SearchResults

	resp, total, err := fetchSearchAllPages(apiClient, search, pageSize)
	if err != nil {
		return SearchResults, total, err
	}

	if err := decodeEntries(resp, &SearchResults); err != nil {
		return SearchResults, total, err
	}

	return SearchResults, total, nil
}

// PerformRawSearchAllPages walks all pages using searchAfter paging and returns the raw
// document maps.
func PerformRawSearchAllPages(apiClient sailpoint.APIClient, search sailpointsdk.Search, pageSize int32) ([]map[string]interface{}, int32, error) {
	return fetchSearchAllPages(apiClient, search, pageSize)
}

// PerformCount returns the number of documents matching the search. It uses the
// dedicated count endpoint and falls back to a single record search that requests
// the X-Total-Count header.
func PerformCount(apiClient sailpoint.APIClient, search sailpointsdk.Search) (int32, error) {
	ctx := context.TODO()

	r, err := apiClient.SearchAPI.SearchCountV1(ctx).Search(search).Execute()
	if err == nil && r != nil {
		if n, ok := totalCountFromResponse(r); ok {
			return n, nil
		}
	} else if err != nil {
		if r == nil || (r.StatusCode != http.StatusNoContent && r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusMethodNotAllowed) {
			return -1, newSearchError(r, err)
		}
	}

	// Fall back to a minimal search, which populates X-Total-Count when count=true.
	_, r, err = apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(1).Offset(0).Count(true).Execute()
	if err != nil {
		return -1, newSearchError(r, err)
	}
	if r == nil {
		return -1, fmt.Errorf("the search count endpoint did not return a response")
	}
	if n, ok := totalCountFromResponse(r); ok {
		return n, nil
	}

	return -1, fmt.Errorf("the search count endpoint did not return a total count")
}

// totalCountFromResponse reads the X-Total-Count header of a response.
func totalCountFromResponse(r *http.Response) (int32, bool) {
	if r == nil {
		return 0, false
	}
	raw := strings.TrimSpace(r.Header.Get("X-Total-Count"))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// newSearchError turns a failed response into an actionable error. A 413 is called out
// explicitly because it is the usual way a whole document search fails on large types.
func newSearchError(r *http.Response, err error) error {
	if r != nil && r.StatusCode == http.StatusRequestEntityTooLarge {
		return fmt.Errorf("413 Content Too Large: the response exceeded the server limit. Retry with --count to only get the number of matches, --includes id,name to return fewer fields, or --limit 50 to return fewer records (original error: %v)", err)
	}
	if err != nil {
		return err
	}
	if r != nil {
		return fmt.Errorf("search failed with status %s", r.Status)
	}
	return fmt.Errorf("search failed")
}

// sortValues returns the values of every sorted field of a document, which is what the
// searchAfter parameter expects. Nested fields such as attributes.uid are supported.
// Returns (nil, false) if any sort field is missing so a partial cursor is never sent.
func sortValues(document map[string]interface{}, sort []string) ([]string, bool) {
	if len(sort) == 0 {
		return nil, false
	}
	values := make([]string, 0, len(sort))
	for _, entry := range sort {
		field := stripSortDirection(entry)
		value := fieldValue(document, field)
		if value == "" {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

// fieldValue walks a dotted field path in a decoded document and renders the value.
func fieldValue(document map[string]interface{}, field string) string {
	var current interface{} = document
	for _, part := range strings.Split(field, ".") {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		value, ok := asMap[part]
		if !ok {
			return ""
		}
		current = value
	}

	switch value := current.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(encoded)
	}
}

// OutputOptions tweaks how results are written by WriteResults. The zero value matches
// the historical behaviour of IterateIndices.
type OutputOptions struct {
	// FileName is the caller chosen file name, without extension. When empty the name
	// is derived from the query and the index.
	FileName string
	// ToStdout writes the JSON to Writer instead of to a file.
	ToStdout bool
	// Writer receives the JSON when ToStdout is set. It defaults to os.Stdout.
	Writer io.Writer
}

// resultGroup is one index worth of results, in a stable order.
type resultGroup struct {
	index string
	value interface{}
}

// resultGroups returns the non-empty per index result sets in a stable order.
func resultGroups(searchResults SearchResults) []resultGroup {
	groups := make([]resultGroup, 0, 6)

	if len(searchResults.AccountActivities) > 0 {
		groups = append(groups, resultGroup{index: "AccountActivities", value: searchResults.AccountActivities})
	}
	if len(searchResults.AccessProfiles) > 0 {
		groups = append(groups, resultGroup{index: "AccessProfiles", value: searchResults.AccessProfiles})
	}
	if len(searchResults.Entitlements) > 0 {
		groups = append(groups, resultGroup{index: "Entitlements", value: searchResults.Entitlements})
	}
	if len(searchResults.Events) > 0 {
		groups = append(groups, resultGroup{index: "Events", value: searchResults.Events})
	}
	if len(searchResults.Identities) > 0 {
		groups = append(groups, resultGroup{index: "Identities", value: searchResults.Identities})
	}
	if len(searchResults.Roles) > 0 {
		groups = append(groups, resultGroup{index: "Roles", value: searchResults.Roles})
	}

	return groups
}

func IterateIndices(SearchResults SearchResults, searchQuery string, folderPath string, outputTypes []string) error {
	if len(SearchResults.AccountActivities) > 0 {
		fileName := BuildResultFileName(searchQuery, "AccountActivities")
		err := SaveResults(SearchResults.AccountActivities, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.AccessProfiles) > 0 {
		fileName := BuildResultFileName(searchQuery, "AccessProfiles")
		err := SaveResults(SearchResults.AccessProfiles, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Entitlements) > 0 {
		fileName := BuildResultFileName(searchQuery, "Entitlements")
		err := SaveResults(SearchResults.Entitlements, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Events) > 0 {
		fileName := BuildResultFileName(searchQuery, "Events")
		err := SaveResults(SearchResults.Events, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Identities) > 0 {
		fileName := BuildResultFileName(searchQuery, "Identities")
		err := SaveResults(SearchResults.Identities, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Roles) > 0 {
		fileName := BuildResultFileName(searchQuery, "Roles")
		err := SaveResults(SearchResults.Roles, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	return nil
}

// normalizeFileName removes a trailing .json extension (case-insensitive) so a user
// passing --file-name report.json gets report.json rather than reportjson.json.
func normalizeFileName(fileName string) string {
	trimmed := strings.TrimSpace(fileName)
	if len(trimmed) >= 5 && strings.EqualFold(trimmed[len(trimmed)-5:], ".json") {
		return trimmed[:len(trimmed)-5]
	}
	return trimmed
}

// WriteResults writes results using the provided options. Unlike IterateIndices it also
// emits a value for an empty result set, so that scripts can tell "0 results" apart from
// a failed run.
func WriteResults(SearchResults SearchResults, searchQuery string, folderPath string, opts OutputOptions) error {
	groups := resultGroups(SearchResults)
	baseName := normalizeFileName(opts.FileName)

	if opts.ToStdout {
		var writer io.Writer = opts.Writer
		if writer == nil {
			writer = os.Stdout
		}
		if len(groups) == 1 {
			return writeJSON(writer, groups[0].value)
		}
		if len(groups) == 0 {
			// An empty result set is still an array, so that 0 results is not confused
			// with a failed run.
			return writeJSON(writer, []interface{}{})
		}
		return writeJSON(writer, SearchResults)
	}

	for _, group := range groups {
		fileName := baseName
		if fileName == "" {
			fileName = BuildResultFileName(searchQuery, group.index)
		} else if len(groups) > 1 {
			fileName = fileName + "-" + group.index
		}
		if err := SaveResults(group.value, fileName, folderPath, []string{"json"}); err != nil {
			return err
		}
	}

	// An empty result set still produces a file so that 0 results is not confused with
	// a failed run.
	if len(groups) == 0 {
		fileName := baseName
		if fileName == "" {
			fileName = BuildResultFileName(searchQuery, "Results")
		}
		return SaveResults([]interface{}{}, fileName, folderPath, []string{"json"})
	}

	return nil
}

type rawResultGroup struct {
	index   string
	jsonKey string
	entries []map[string]interface{}
}

func rawIndexNames(docType string) (string, string) {
	switch docType {
	case "accountactivity":
		return "AccountActivities", "accountActivities"
	case "accessprofile":
		return "AccessProfiles", "accessProfiles"
	case "entitlement":
		return "Entitlements", "entitlements"
	case "event":
		return "Events", "events"
	case "identity":
		return "Identities", "identities"
	case "role":
		return "Roles", "roles"
	case "":
		return "Results", "results"
	default:
		return docType, docType
	}
}

func groupRawEntries(entries []map[string]interface{}) []rawResultGroup {
	if len(entries) == 0 {
		return nil
	}

	byIndex := make(map[string]*rawResultGroup)
	order := make([]string, 0, 2)

	for _, entry := range entries {
		docType, _ := entry["_type"].(string)
		indexName, jsonKey := rawIndexNames(docType)
		g, ok := byIndex[indexName]
		if !ok {
			g = &rawResultGroup{index: indexName, jsonKey: jsonKey}
			byIndex[indexName] = g
			order = append(order, indexName)
		}
		g.entries = append(g.entries, entry)
	}

	groups := make([]rawResultGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, *byIndex[key])
	}
	return groups
}

// WriteRawResults writes raw document maps (used when --includes is set) so custom
// attributes are preserved and omitted fields are not zero-filled.
func WriteRawResults(entries []map[string]interface{}, searchQuery string, folderPath string, opts OutputOptions) error {
	groups := groupRawEntries(entries)
	baseName := normalizeFileName(opts.FileName)

	if opts.ToStdout {
		var writer io.Writer = opts.Writer
		if writer == nil {
			writer = os.Stdout
		}
		if len(groups) == 0 {
			return writeJSON(writer, []interface{}{})
		}
		if len(groups) == 1 {
			return writeJSON(writer, groups[0].entries)
		}
		payload := make(map[string][]map[string]interface{}, len(groups))
		for _, group := range groups {
			payload[group.jsonKey] = group.entries
		}
		return writeJSON(writer, payload)
	}

	if len(groups) == 0 {
		fileName := baseName
		if fileName == "" {
			fileName = BuildResultFileName(searchQuery, "Results")
		}
		return SaveResults([]interface{}{}, fileName, folderPath, []string{"json"})
	}

	for _, group := range groups {
		fileName := baseName
		if fileName == "" {
			fileName = BuildResultFileName(searchQuery, group.index)
		} else if len(groups) > 1 {
			fileName = fileName + "-" + group.index
		}
		if err := SaveResults(group.entries, fileName, folderPath, []string{"json"}); err != nil {
			return err
		}
	}

	return nil
}

// BuildResultFileName builds a result file name that is safe for any filesystem by
// truncating long query text on a UTF-8 rune boundary and keeping a short hash of the
// full name.
func BuildResultFileName(searchQuery string, index string) string {
	name := "query=" + searchQuery + "&indices=" + index
	if len(name) <= maxAutoFileNameLength {
		return name
	}

	sum := sha1.Sum([]byte(name))
	tail := hex.EncodeToString(sum[:])
	if len(tail) > 8 {
		tail = tail[:8]
	}

	maxPrefixBytes := maxAutoFileNameLength - len(tail) - 1
	prefix := truncateUTF8(name, maxPrefixBytes)
	return prefix + "-" + tail
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		if i+size > maxBytes {
			break
		}
		i += size
		cut = i
	}
	return s[:cut]
}

// writeJSON renders a value as indented JSON on the given writer.
func writeJSON(writer io.Writer, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = writer.Write(append(data, '\n'))
	return err
}

func SaveResults[T any](formattedResponse T, fileName string, filePath string, outputTypes []string) error {
	for i := 0; i < len(outputTypes); i++ {
		outputType := outputTypes[i]
		switch outputType {
		case "json":
			savePath := output.GetSanitizedPath(fileName, "json")
			log.Info("Saving Results", "file", savePath)
			err := output.SaveJSONFile(formattedResponse, fileName, filePath)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("invalid output type provided %s", outputType)
		}
	}

	return nil
}
