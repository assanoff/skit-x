// Package idempotency wraps the REST routes and the broker consumers of the
// service in dedup: the recipes of the dedup package written on the types of
// skit. It is application code — dedup itself knows no other package of skit.
package idempotency

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/dedup"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/rest"
)

// Header carries the idempotency key of a POST.
const Header = "Idempotency-Key"

// anonymous owns the keys of a request without a principal (auth off).
const anonymous = "anonymous"

var (
	// errNotStored marks a response that is not stored: a retry runs the
	// handler again.
	errNotStored = errors.New("idempotency: the response is not stored")
	// errNotAcked marks a verdict other than Ack: the redelivery runs the
	// handler again.
	errNotAcked = errors.New("idempotency: the message is not acked")
)

// HTTP returns application middleware that runs a POST once per
// Idempotency-Key of its caller and answers its retries with the stored
// response — the same status, content type and body. The key is scoped by the
// route and by the subject of the principal ("anonymous" with auth off), so
// callers never share keys; place the middleware inside the auth guard. Other
// methods and a POST without the header pass through.
//
// An error response, a 5xx and a response the handler wrote itself
// (rest.NoResponse) are not stored: a retry runs the handler again. A retry
// while the first request runs gets 409 with Retry-After, a store that is down
// 503.
func HTTP(k *dedup.Keeper) rest.MidFunc {
	return func(next rest.HandlerFunc) rest.HandlerFunc {
		return func(ctx context.Context, r *http.Request) rest.ResponseEncoder {
			header := r.Header.Get(Header)
			if r.Method != http.MethodPost || header == "" {
				return next(ctx, r)
			}

			var live rest.ResponseEncoder
			stored, err := k.Do(ctx, httpKey(ctx, r, header), func(ctx context.Context) (response, error) {
				live = next(ctx, r)
				return record(live)
			})
			if live != nil && err != nil {
				return live // the handler ran, and its response is not stored
			}
			if errors.Is(err, dedup.ErrInProgress) {
				retryAfter(ctx)
				return errs.Newf(errs.Aborted, "a request with this %s is in progress", Header)
			}
			if errors.Is(err, dedup.ErrUnavailable) {
				return errs.New(errs.Unavailable, err)
			}
			if err != nil {
				return errs.New(errs.Internal, err)
			}
			return stored
		}
	}
}

// response is what HTTP stores for a key. It is a rest.ResponseEncoder, so a
// retry is answered with it as it is.
type response struct {
	Status int    `json:"status"`
	Type   string `json:"type"`
	Body   []byte `json:"body"`
}

// Encode implements rest.ResponseEncoder.
func (r response) Encode() ([]byte, string, error) {
	return r.Body, r.Type, nil
}

// HTTPStatus sets the status of the response.
func (r response) HTTPStatus() int { return r.Status }

// record turns the response of the handler into the one stored; an error
// means it is not stored.
func record(resp rest.ResponseEncoder) (response, error) {
	if resp == nil {
		return response{Status: http.StatusNoContent}, nil
	}
	if err, ok := resp.(error); ok {
		return response{}, err
	}
	if _, ok := resp.(rest.NoResponse); ok {
		return response{}, errNotStored
	}
	body, contentType, err := resp.Encode()
	if err != nil {
		return response{}, err
	}
	stored := response{Status: http.StatusOK, Type: contentType, Body: body}
	if s, ok := resp.(interface{ HTTPStatus() int }); ok {
		stored.Status = s.HTTPStatus()
	}
	if stored.Status >= http.StatusInternalServerError {
		return response{}, errNotStored
	}
	return stored, nil
}

// httpKey is the route, the owner and the header, each escaped so that none
// holds the separator.
func httpKey(ctx context.Context, r *http.Request, header string) string {
	owner := anonymous
	if p, ok := auth.PrincipalFromContext(ctx); ok && p.Subject != "" {
		owner = p.Subject
	}
	return join("http", r.Pattern, owner, header)
}

// retryAfter asks the client to retry in a second.
func retryAfter(ctx context.Context) {
	if w := rest.GetWriter(ctx); w != nil {
		w.Header().Set("Retry-After", "1")
	}
}

// Handler returns a broker.Handler that runs h once per message of group,
// the queue its replicas compete on. The key is the group, the source and the
// id of the message, so the replicas of a group share the records and other
// groups do not; a message without an id goes straight to h.
//
// A verdict other than Ack is an error of the call: the key is freed and the
// redelivery runs h again. A repeat of a handled message is acked; a busy or
// unavailable store requeues it.
func Handler(k *dedup.Keeper, group string, h broker.Handler) broker.Handler {
	return func(ctx context.Context, m broker.Message) broker.Action {
		if m.ID == "" {
			return h(ctx, m)
		}
		verdict := broker.Ack
		_, err := k.Do(ctx, join("broker", group, m.Source, m.ID), func(ctx context.Context) (struct{}, error) {
			verdict = h(ctx, m)
			if verdict != broker.Ack {
				return struct{}{}, errNotAcked
			}
			return struct{}{}, nil
		})
		if errors.Is(err, errNotAcked) {
			return verdict
		}
		if err != nil {
			return broker.Requeue
		}
		return broker.Ack
	}
}

// join escapes each part and joins them with "/", so no part holds the
// separator.
func join(parts ...string) string {
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
