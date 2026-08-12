package dsp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPushCallbackSendsJSON(t *testing.T) {
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode pushed body: %v", err)
		}
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pushCallback(srv.URL, map[string]string{"hello": "world"})

	select {
	case body := <-received:
		if body["hello"] != "world" {
			t.Errorf("received %v, want {hello: world}", body)
		}
	default:
		t.Fatal("pushCallback did not send a request before returning")
	}
}

func TestPushCallbackToUnreachableURLDoesNotPanic(t *testing.T) {
	pushCallback("http://127.0.0.1:1/unreachable", map[string]string{"hello": "world"})
}
