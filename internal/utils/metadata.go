package utils

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	tokenURL     = "http://100.100.100.200/latest/api/token"
	tokenTimeout = 21600
)

func GetStrFromMetadata(url string) (string, error) {
	body, err := getWithToken(url)
	if err != nil {
		return "", err
	}
	result := strings.Split(string(body), "\n")
	trimResult := strings.Trim(result[0], "/")
	return trimResult, nil
}

type Error struct {
	URL  string
	Code string
	R    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("get from metaserver failed code: %s, url: %s, err: %s", e.Code, e.URL, e.R)
}

var (
	tokenCache    *cache.Expiring
	single        singleflight.Group
	defaultClient *http.Client
)

func getWithToken(url string) ([]byte, error) {

	skipRetry := false
retry:
	var token string
	v, ok := tokenCache.Get(tokenURL)
	if !ok {
		vv, err, _ := single.Do(tokenURL, func() (interface{}, error) {
			out, err := withRetry(tokenURL, [][]string{
				{
					"X-aliyun-ecs-metadata-token-ttl-seconds", strconv.Itoa(tokenTimeout),
				},
			})
			if err != nil {
				return nil, err
			}
			return string(out), nil
		})
		if err != nil {
			return nil, err
		}

		token = vv.(string)

		tokenCache.Set(tokenURL, token, tokenTimeout*time.Second/2)
	} else {
		token = v.(string)
	}

	out, err := withRetry(url, [][]string{
		{
			"X-aliyun-ecs-metadata-token", token,
		},
	})
	if err != nil {
		var typedErr *Error
		ok := errors.As(err, &typedErr)

		if ok && !skipRetry {
			if typedErr.Code == strconv.Itoa(http.StatusUnauthorized) {
				skipRetry = true

				tokenCache.Delete(tokenURL)
				goto retry
			}
		}

		return nil, err
	}

	return out, err
}

func withRetry(url string, headers [][]string) ([]byte, error) {
	var innerErr error
	var body []byte
	err := wait.ExponentialBackoff(wait.Backoff{
		Duration: 500 * time.Millisecond,
		Factor:   1.2,
		Jitter:   0.1,
		Steps:    4,
	}, func() (bool, error) {
		var err error

		method := "GET"
		if url == tokenURL {
			method = "PUT"
		}
		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			innerErr = &Error{
				URL: url,
				R:   err,
			}
			return false, nil
		}

		for _, h := range headers {
			if len(h) != 2 {
				return false, fmt.Errorf("invalid header")
			}
			req.Header.Set(h[0], h[1])
		}

		resp, err := defaultClient.Do(req)
		if err != nil {
			// retryable err
			innerErr = &Error{
				URL: url,
				R:   err,
			}
			return false, nil
		}
		defer resp.Body.Close() // nolint:errcheck

		// retryable err
		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= http.StatusInternalServerError {
			innerErr = &Error{
				URL:  url,
				Code: strconv.Itoa(resp.StatusCode),
				R:    nil,
			}
			return false, nil
		}

		if resp.StatusCode >= http.StatusBadRequest {
			innerErr = &Error{
				URL:  url,
				Code: strconv.Itoa(resp.StatusCode),
				R:    nil,
			}
			return false, innerErr
		}

		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		if innerErr != nil {
			return nil, innerErr
		}
		return nil, err
	}
	return body, nil
}

func init() {
	tokenCache = cache.NewExpiring()
	defaultClient = &http.Client{
		Transport:     nil,
		CheckRedirect: nil,
		Jar:           nil,
		Timeout:       30 * time.Second,
	}
}
