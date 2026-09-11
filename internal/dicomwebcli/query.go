package dicomwebcli

import (
	"context"
	"flag"
	"io"
	"net/url"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

type queryOptions struct {
	common    commonOptions
	studyUID  string
	seriesUID string
	queries   stringList
}

func runQuery(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts := queryOptions{common: defaultCommonOptions()}
	fs := flag.NewFlagSet("dicomweb query", flag.ContinueOnError)
	addCommonFlags(fs, &opts.common)
	fs.StringVar(&opts.studyUID, "study-uid", "", "study scope UID")
	fs.StringVar(&opts.seriesUID, "series-uid", "", "series scope UID")
	fs.Var(&opts.queries, "query", "QIDO key=value parameter (repeatable)")
	configureUsage(fs, stderr, "dicomweb query [flags] <studies|series|instances>")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return invalidInput("query requires one resource")
	}
	params, err := queryValues(opts.queries)
	if err != nil {
		return err
	}
	client, err := opts.common.client()
	if err != nil {
		return err
	}
	studyUID := strings.TrimSpace(opts.studyUID)
	seriesUID := strings.TrimSpace(opts.seriesUID)
	var datasets []dicomweb.Dataset
	switch fs.Arg(0) {
	case "studies":
		if studyUID != "" || seriesUID != "" {
			return invalidInput("study and series scope are not valid for studies")
		}
		datasets, err = client.SearchStudies(ctx, params)
	case "series":
		if !core.IsValidUID(studyUID) || seriesUID != "" {
			return invalidInput("series query requires only a valid -study-uid")
		}
		datasets, err = client.SearchSeries(ctx, studyUID, params)
	case "instances":
		if !core.IsValidUID(studyUID) || !core.IsValidUID(seriesUID) {
			return invalidInput("instance query requires valid study and series UIDs")
		}
		datasets, err = client.SearchInstances(ctx, studyUID, seriesUID, params)
	default:
		return invalidInput("query resource must be studies, series, or instances")
	}
	if err != nil {
		return err
	}
	if datasets == nil {
		datasets = make([]dicomweb.Dataset, 0)
	}
	return writeJSON(stdout, opts.common.output, datasets)
}

func queryValues(values []string) (url.Values, error) {
	params := make(url.Values)
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, invalidInput("each -query must use key=value")
		}
		params.Add(key, item)
	}
	return params, nil
}
