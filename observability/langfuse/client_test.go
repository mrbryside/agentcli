package langfuse

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientExportsOTLPHTTPWithLangfuseHeaders(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests <- request.Clone(context.Background())
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(context.Background(), Config{
		BaseURL:     server.URL,
		PublicKey:   "pk-test",
		SecretKey:   "sk-test",
		ServiceName: "test-service",
		SampleRate:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := client.tracer.Start(context.Background(), "test")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case request := <-requests:
		if request.URL.Path != "/api/public/otel/v1/traces" {
			t.Errorf("path = %q", request.URL.Path)
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("pk-test:sk-test"))
		if got := request.Header.Get("Authorization"); got != wantAuth {
			t.Errorf("Authorization = %q, want %q", got, wantAuth)
		}
		if got := request.Header.Get("x-langfuse-ingestion-version"); got != "4" {
			t.Errorf("ingestion version = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OTLP export")
	}
}
