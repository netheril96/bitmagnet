package tmdb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/go-resty/resty/v2"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"
)

// requesterLazy defers instantiation of the requester (and possible failure) until the first request is made,
// avoiding failure when the TMDB client is not needed.
type requesterLazy struct {
	once      sync.Once
	config    Config
	logger    *zap.SugaredLogger
	err       error
	requester Requester
}

func (r *requesterLazy) Request(
	ctx context.Context,
	path string,
	queryParams map[string]string,
	result interface{},
) (*resty.Response, error) {
	r.once.Do(func() {
		r.requester, r.err = newRequester(ctx, r.config, r.logger)
	})

	if r.err != nil {
		return nil, r.err
	}

	return r.requester.Request(ctx, path, queryParams, result)
}

func newRequester(ctx context.Context, config Config, logger *zap.SugaredLogger) (Requester, error) {
	if !config.Enabled {
		return nil, errors.New("TMDB is disabled")
	}

	if config.APIKey == defaultTmdbAPIKey {
		logger.Warnln(
			"you are using the default TMDB api key; TMDB requests will be limited to 1 per second; " +
				"to remove this warning please configure a personal TMDB api key",
		)

		config.RateLimit = time.Second
		config.RateLimitBurst = 8
	}

	var limiter = rate.NewLimiter(rate.Every(config.RateLimit), config.RateLimitBurst)
	var semaphore = semaphore.NewWeighted(2)

	r := requesterLogger{
		requester: requesterFailFast{
			requester: requester{
				resty: resty.New().
					SetBaseURL(config.BaseURL).
					SetQueryParam("api_key", config.APIKey).
					SetRetryCount(3).
					SetRetryWaitTime(2 * time.Second).
					SetRetryMaxWaitTime(20 * time.Second).
					SetTimeout(10 * time.Second).
					EnableTrace().
					SetLogger(logger).
					OnBeforeRequest(func(c *resty.Client, r *resty.Request) error {
						if err := semaphore.Acquire(ctx, 1); err != nil {
							return err
						}
						if err := limiter.Wait(ctx); err != nil {
							semaphore.Release(1)
							return err
						}
						return nil
					}).
					OnAfterResponse(func(c *resty.Client, r *resty.Response) error {
						semaphore.Release(1)
						return nil
					}),
			},
			isUnauthorized: &concurrency.AtomicValue[bool]{},
		},
		logger: logger,
	}

	err := client{r}.ValidateAPIKey(ctx)
	if errors.Is(err, ErrUnauthorized) {
		if config.APIKey == defaultTmdbAPIKey {
			return r, fmt.Errorf("default api key is invalid: %w", err)
		}

		logger.Errorw("invalid api key, falling back to default", "error", err)

		config.APIKey = defaultTmdbAPIKey

		return newRequester(ctx, config, logger)
	}

	return r, err
}
