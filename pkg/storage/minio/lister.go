package minio

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/observ"
	modpath "golang.org/x/mod/module"
)

func (s *storageImpl) List(ctx context.Context, module string) ([]string, error) {
	const op errors.Op = "minio.List"
	_, span := observ.StartSpan(ctx, op.String())
	defer span.End()
	// Versions live at <module>/<version>/. List only that level (delimiter
	// "/") so nested modules' objects don't count, and follow continuation:
	// a single page stops at 1000 entries.
	searchPrefix := module + "/"
	var ret []string
	token := ""
	for {
		res, err := s.minioCore.ListObjectsV2(s.bucketName, searchPrefix, token, false, "/", 0, "")
		if err != nil {
			return nil, errors.E(op, err, errors.M(module))
		}
		for _, p := range res.CommonPrefixes {
			ver := strings.TrimSuffix(strings.TrimPrefix(p.Prefix, searchPrefix), "/")
			if unescaped, err := url.PathUnescape(ver); err == nil {
				ver = unescaped
			}
			// Nested module directories ("service/", major versions like
			// "v2/") are not canonical versions.
			if ver != "" && modpath.CanonicalVersion(ver) == ver {
				ret = append(ret, ver)
			}
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			break
		}
		token = res.NextContinuationToken
	}
	sort.Strings(ret)
	return ret, nil
}
