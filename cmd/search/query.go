// Copyright (c) 2021, SailPoint Technologies, Inc. All rights reserved.
package search

import (
	"fmt"

	"github.com/charmbracelet/log"
	sailpoint "github.com/sailpoint-oss/golang-sdk/v3"
	sailpointsdk "github.com/sailpoint-oss/golang-sdk/v3/search"
	"github.com/sailpoint-oss/sailpoint-cli/internal/config"
	"github.com/sailpoint-oss/sailpoint-cli/internal/search"
	"github.com/spf13/cobra"
)

func newQueryCmd() *cobra.Command {
	var indices []string
	var sort []string
	var searchQuery string
	var folderPath string
	var includes []string
	var fileName string
	var limit int32
	var offset int32
	var count bool
	var pageAll bool
	var toStdout bool
	cmd := &cobra.Command{
		Use:     "query",
		Short:   "Manually search using a specific query and indices",
		Long:    "\nRun a search query in SailPoint Identity Security, using a specific query and indicies\n\n",
		Example: "sail search query \"(type:provisioning AND created:[now-90d TO now])\" --indices events",
		Aliases: []string{"que"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateQueryFlags(cmd, count, pageAll, limit, offset); err != nil {
				return err
			}

			err := config.InitConfig()
			if err != nil {
				return err
			}

			apiClient, err := config.InitAPIClient(false)
			if err != nil {
				return err
			}

			searchQuery = args[0]

			searchObj, err := search.BuildSearch(searchQuery, sort, indices)
			if err != nil {
				return err
			}

			// ApplyIncludes is a no-op when --includes is empty, so the request stays
			// identical to the previous behaviour.
			search.ApplyIncludes(&searchObj, includes)

			log.Info("Performing Search", "Query", searchQuery, "Indices", indices)

			// Without any of the new flags the historical code path is used, so existing
			// scripts keep the same files and the same number of records.
			if !usesExtendedFlags(cmd) {
				formattedResponse, err := search.PerformSearch(*apiClient, searchObj)
				if err != nil {
					return err
				}

				return search.IterateIndices(formattedResponse, searchQuery, folderPath, []string{"json"})
			}

			if count {
				total, err := search.PerformCount(*apiClient, searchObj)
				if err != nil {
					return err
				}

				fmt.Fprintln(cmd.OutOrStdout(), total)
				return nil
			}

			opts := search.OutputOptions{
				FileName: fileName,
				ToStdout: toStdout,
				Writer:   cmd.OutOrStdout(),
			}

			// When --includes is set, preserve raw documents so requested custom
			// attributes are not dropped and omitted fields are not zero-filled.
			if searchObj.QueryResultFilter != nil && len(searchObj.QueryResultFilter.GetIncludes()) > 0 {
				rawEntries, err := runPagedRawSearch(cmd, apiClient, searchObj, pageAll, limit, offset)
				if err != nil {
					return err
				}

				return search.WriteRawResults(rawEntries, searchQuery, folderPath, opts)
			}

			formattedResponse, err := runPagedSearch(cmd, apiClient, searchObj, pageAll, limit, offset)
			if err != nil {
				return err
			}

			return search.WriteResults(formattedResponse, searchQuery, folderPath, opts)
		},
	}

	cmd.Flags().StringVarP(&folderPath, "folderPath", "f", "search_results", "Folder path to save the search results to. If the directory doesn't exist, then it will be created. (defaults to the current working directory)")
	cmd.Flags().StringArrayVar(&indices, "indices", []string{}, "Indices to perform the search query on (accessprofiles, accountactivities, entitlements, events, identities, roles)")
	cmd.Flags().StringArrayVar(&sort, "sort", []string{}, "The sort value for the api call (displayName, +id...)")
	cmd.Flags().StringSliceVar(&includes, "includes", []string{}, "Comma separated list of fields to return, for example id,name,attributes.uid. Only those fields are returned instead of whole documents.")
	cmd.Flags().StringVar(&fileName, "file-name", "", "Name to use for the result file, without extension. Defaults to a name derived from the query.")
	cmd.Flags().Int32Var(&limit, "limit", 0, "Maximum number of results to return per request (defaults to 250 when --offset or --page-all is used).")
	cmd.Flags().Int32Var(&offset, "offset", 0, "Position in the full result set to start from.")
	cmd.Flags().BoolVar(&count, "count", false, "Print only the total number of matches to stdout and write no file.")
	cmd.Flags().BoolVar(&pageAll, "page-all", false, "Follow searchAfter paging until the whole result set is retrieved, which is not limited to the first 10K records.")
	cmd.Flags().BoolVar(&toStdout, "stdout", false, "Print the results as JSON on stdout instead of writing a file.")
	cmd.MarkFlagRequired("indices")

	return cmd
}

// validateQueryFlags rejects negative paging values and incompatible flag combinations.
func validateQueryFlags(cmd *cobra.Command, count bool, pageAll bool, limit int32, offset int32) error {
	if limit < 0 {
		return fmt.Errorf("--limit must be 0 or greater, got %d", limit)
	}
	if offset < 0 {
		return fmt.Errorf("--offset must be 0 or greater, got %d", offset)
	}
	if pageAll && cmd.Flags().Changed("offset") {
		return fmt.Errorf("--page-all cannot be combined with --offset")
	}
	if count {
		for _, name := range []string{"page-all", "limit", "offset", "file-name"} {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--count cannot be combined with --%s", name)
			}
		}
	}
	return nil
}

// usesExtendedFlags reports whether any of the flags that select a new code path were set.
func usesExtendedFlags(cmd *cobra.Command) bool {
	for _, name := range []string{"includes", "file-name", "limit", "offset", "count", "page-all", "stdout"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// runPagedSearch retrieves decoded results for the extended flag combination.
func runPagedSearch(cmd *cobra.Command, apiClient *sailpoint.APIClient, searchObj sailpointsdk.Search, pageAll bool, limit int32, offset int32) (search.SearchResults, error) {
	if pageAll {
		results, total, err := search.PerformSearchAllPagesWithLimit(*apiClient, searchObj, limit)
		logTotal(total)
		return results, err
	}

	if cmd.Flags().Changed("limit") || cmd.Flags().Changed("offset") {
		results, total, err := search.PerformSearchPage(*apiClient, searchObj, limit, offset)
		logTotal(total)
		return results, err
	}

	return search.PerformSearch(*apiClient, searchObj)
}

// runPagedRawSearch retrieves raw document maps for queries that use --includes.
func runPagedRawSearch(cmd *cobra.Command, apiClient *sailpoint.APIClient, searchObj sailpointsdk.Search, pageAll bool, limit int32, offset int32) ([]map[string]interface{}, error) {
	if pageAll {
		results, total, err := search.PerformRawSearchAllPages(*apiClient, searchObj, limit)
		logTotal(total)
		return results, err
	}

	if cmd.Flags().Changed("limit") || cmd.Flags().Changed("offset") {
		results, total, err := search.PerformRawSearchPage(*apiClient, searchObj, limit, offset)
		logTotal(total)
		return results, err
	}

	return search.PerformRawSearch(*apiClient, searchObj)
}

func logTotal(total int32) {
	if total >= 0 {
		log.Debug("Total matches", "count", total)
	}
}
