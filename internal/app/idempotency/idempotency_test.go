package idempotency_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matryer/is"

	"github.com/assanoff/skit/auth"
	"github.com/assanoff/skit/broker"
	"github.com/assanoff/skit/dedup"
	"github.com/assanoff/skit/errs"
	"github.com/assanoff/skit/rest"

	"github.com/assanoff/skit-x/internal/app/idempotency"
)

// created counts its calls and answers 201 with the count.
func created(calls *int) rest.HandlerFunc {
	return func(context.Context, *http.Request) rest.ResponseEncoder {
		*calls++
		return rest.JSONStatus(map[string]int{"n": *calls}, http.StatusCreated)
	}
}

// serve runs h on a request of method with the Idempotency-Key key, as the
// principal subject when it is not empty.
func serve(h rest.HandlerFunc, method, key, subject string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/products", nil)
	r.Pattern = method + " /products"
	if key != "" {
		r.Header.Set(idempotency.Header, key)
	}
	if subject != "" {
		r = r.WithContext(auth.WithPrincipal(r.Context(), &auth.Principal{Subject: subject}))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPRetryGetsStoredResponse(t *testing.T) {
	is := is.New(t)
	calls := 0
	h := idempotency.HTTP(dedup.NewMemory())(created(&calls))

	first := serve(h, http.MethodPost, "k1", "user-7")
	retry := serve(h, http.MethodPost, "k1", "user-7")
	is.Equal(calls, 1) // the handler runs once
	is.Equal(retry.Code, http.StatusCreated)
	is.Equal(retry.Body.String(), first.Body.String()) // the same body
	is.Equal(retry.Header().Get("Content-Type"), first.Header().Get("Content-Type"))
}

func TestHTTPPassesThrough(t *testing.T) {
	is := is.New(t)
	calls := 0
	h := idempotency.HTTP(dedup.NewMemory())(created(&calls))

	serve(h, http.MethodPost, "", "user-7")
	serve(h, http.MethodPost, "", "user-7")
	is.Equal(calls, 2) // a POST without the header
	serve(h, http.MethodPut, "k1", "user-7")
	serve(h, http.MethodPut, "k1", "user-7")
	is.Equal(calls, 4) // another method
}

func TestHTTPKeysOfCallers(t *testing.T) {
	is := is.New(t)
	calls := 0
	h := idempotency.HTTP(dedup.NewMemory())(created(&calls))

	serve(h, http.MethodPost, "k1", "user-7")
	serve(h, http.MethodPost, "k1", "user-8")
	is.Equal(calls, 2) // callers never share keys
	serve(h, http.MethodPost, "k1", "")
	serve(h, http.MethodPost, "k1", "")
	is.Equal(calls, 3) // the anonymous callers share theirs
}

func TestHTTPErrorIsNotStored(t *testing.T) {
	for name, resp := range map[string]rest.ResponseEncoder{
		"error": errs.Newf(errs.InvalidArgument, "bad"),
		"5xx":   rest.JSONStatus(map[string]string{}, http.StatusBadGateway),
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			calls := 0
			h := idempotency.HTTP(dedup.NewMemory())(func(context.Context, *http.Request) rest.ResponseEncoder {
				calls++
				return resp
			})

			first := serve(h, http.MethodPost, "k1", "user-7")
			serve(h, http.MethodPost, "k1", "user-7")
			is.Equal(calls, 2) // a retry runs the handler again
			is.True(first.Code >= http.StatusBadRequest)
		})
	}
}

func TestHTTPInProgress(t *testing.T) {
	is := is.New(t)
	k := dedup.NewMemory()
	running, finish := make(chan struct{}), make(chan struct{})
	slow := idempotency.HTTP(k)(func(context.Context, *http.Request) rest.ResponseEncoder {
		close(running)
		<-finish
		return nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(slow, http.MethodPost, "k1", "user-7")
	}()
	<-running

	calls := 0
	w := serve(idempotency.HTTP(k)(created(&calls)), http.MethodPost, "k1", "user-7")
	is.Equal(w.Code, http.StatusConflict) // the first request still runs
	is.Equal(w.Header().Get("Retry-After"), "1")
	is.Equal(calls, 0)
	close(finish)
	<-done
}

func TestHTTPStoreUnavailable(t *testing.T) {
	is := is.New(t)
	k := closedRedis(t)
	calls := 0
	w := serve(idempotency.HTTP(k)(created(&calls)), http.MethodPost, "k1", "user-7")
	is.Equal(w.Code, http.StatusServiceUnavailable)
	is.Equal(calls, 0)
}

// acker counts its calls and returns verdict.
func acker(calls *int, verdict broker.Action) broker.Handler {
	return func(context.Context, broker.Message) broker.Action {
		*calls++
		return verdict
	}
}

func TestHandlerRepeatIsAcked(t *testing.T) {
	is := is.New(t)
	k := dedup.NewMemory()
	calls := 0
	h := idempotency.Handler(k, "audit", acker(&calls, broker.Ack))
	m := broker.Message{ID: "e1", Source: "shop"}

	is.Equal(h(t.Context(), m), broker.Ack)
	is.Equal(h(t.Context(), m), broker.Ack) // the redelivery is acked
	is.Equal(calls, 1)                      // without the handler

	other := idempotency.Handler(k, "billing", acker(&calls, broker.Ack))
	is.Equal(other(t.Context(), m), broker.Ack)
	is.Equal(calls, 2) // another group runs its own handler
}

func TestHandlerVerdictFreesKey(t *testing.T) {
	is := is.New(t)
	calls := 0
	h := idempotency.Handler(dedup.NewMemory(), "audit", acker(&calls, broker.Requeue))
	m := broker.Message{ID: "e1", Source: "shop"}

	is.Equal(h(t.Context(), m), broker.Requeue) // the verdict of the handler
	is.Equal(h(t.Context(), m), broker.Requeue)
	is.Equal(calls, 2) // the redelivery runs the handler again
}

func TestHandlerWithoutID(t *testing.T) {
	is := is.New(t)
	calls := 0
	h := idempotency.Handler(dedup.NewMemory(), "audit", acker(&calls, broker.Ack))

	h(t.Context(), broker.Message{Source: "shop"})
	h(t.Context(), broker.Message{Source: "shop"})
	is.Equal(calls, 2) // a message without an id goes straight to the handler
}

func TestHandlerStoreUnavailable(t *testing.T) {
	is := is.New(t)
	calls := 0
	h := idempotency.Handler(closedRedis(t), "audit", acker(&calls, broker.Ack))

	is.Equal(h(t.Context(), broker.Message{ID: "e1"}), broker.Requeue)
	is.Equal(calls, 0)
}

// closedRedis is a Keeper whose Redis client is closed: every call finds the
// store unavailable.
func closedRedis(t *testing.T) *dedup.Keeper {
	t.Helper()
	k, err := dedup.New("redis://127.0.0.1:6379/0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	return k
}
