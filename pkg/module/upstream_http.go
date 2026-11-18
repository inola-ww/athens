package module

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gomods/athens/pkg/errors"
)

// maxMetaBody caps how much of an upstream list, .info or .mod response is read.
const maxMetaBody = 4 << 20

// upstreamGet opens url on an upstream module proxy. 404 and 410 map to
// KindNotFound, a deadline to KindGatewayTimeout. The caller closes the body.
func upstreamGet(ctx context.Context, client *http.Client, url string) (io.ReadCloser, error) {
	const op errors.Op = "module.upstreamGet"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.E(op, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.IsErr(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.E(op, err, errors.KindGatewayTimeout)
		}
		return nil, errors.E(op, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		return nil, errors.E(op, fmt.Sprintf("%s: %s", url, strings.TrimSpace(string(msg))), errors.KindNotFound)
	case http.StatusTooManyRequests:
		return nil, errors.E(op, fmt.Sprintf("%s: rate limited", url), errors.KindRateLimit)
	default:
		return nil, errors.E(op, fmt.Sprintf("%s: unexpected status %d", url, resp.StatusCode), errors.KindUnexpected)
	}
}

// upstreamGetBytes reads a small upstream response fully.
func upstreamGetBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	const op errors.Op = "module.upstreamGetBytes"
	body, err := upstreamGet(ctx, client, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(io.LimitReader(body, maxMetaBody))
	if err != nil {
		return nil, errors.E(op, err)
	}
	return b, nil
}
