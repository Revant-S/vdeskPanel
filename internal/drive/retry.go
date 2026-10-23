package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v5"
	"google.golang.org/api/googleapi"
)

type TokenRefresher interface {
	RefreshToken(ctx context.Context) error
}

// callWithRetry runs one Drive request under a budget bucket (nil for content transfer)
// with full-jitter retries on rate limits, 5xx and transport failures.
func callWithRetry[T any](ctx context.Context, c *Client, bucket *adaptiveBucket, operation string, attempt func(context.Context) (T, error)) (T, error) {
	refreshed := false
	result, err := backoff.Retry(ctx, func() (T, error) {
		if bucket != nil {
			if err := bucket.wait(ctx); err != nil {
				var zero T
				return zero, backoff.Permanent(err)
			}
		}
		attemptCtx, cancel := c.attemptContext(ctx)
		result, err := attempt(attemptCtx)
		cancel()
		if err == nil {
			return result, nil
		}
		return result, c.classifyFailure(ctx, bucket, normalizeError(err), &refreshed)
	},
		backoff.WithBackOff(newFullJitterBackOff(c.retry)),
		backoff.WithMaxTries(c.retry.MaxAttempts),
		backoff.WithMaxElapsedTime(0),
		backoff.WithNotify(func(err error, delay time.Duration) {
			c.logger.DebugContext(ctx, "drive request retry", "operation", operation, "status", statusOf(err), "delay", delay)
		}),
	)
	if err != nil {
		return result, fmt.Errorf("%s: %w", operation, err)
	}
	return result, nil
}

// classifyFailure returns err as-is when the request should be retried, or wrapped as
// permanent when it should not.
func (c *Client) classifyFailure(ctx context.Context, bucket *adaptiveBucket, err error, refreshed *bool) error {
	if ctx.Err() != nil {
		return backoff.Permanent(err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.isRateLimit():
		if bucket != nil {
			bucket.penalize()
		}
		return err
	case apiErr.Status >= http.StatusInternalServerError:
		return err
	case apiErr.Status == http.StatusUnauthorized && !*refreshed && c.refresher != nil:
		*refreshed = true
		if refreshErr := c.refresher.RefreshToken(ctx); refreshErr != nil {
			return backoff.Permanent(fmt.Errorf("refresh token: %w", errors.Join(refreshErr, err)))
		}
		return err
	}
	return backoff.Permanent(err)
}

func normalizeError(err error) error {
	var googleErr *googleapi.Error
	if !errors.As(err, &googleErr) {
		return err
	}
	apiErr := &APIError{Status: googleErr.Code, Message: googleErr.Message}
	if len(googleErr.Errors) > 0 {
		apiErr.Reason = googleErr.Errors[0].Reason
	}
	return apiErr
}

func statusOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

func (c *Client) attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.retry.AttemptTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.retry.AttemptTimeout)
}
