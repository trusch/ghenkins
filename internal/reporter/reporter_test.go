package reporter_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v67/github"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trusch/ghenkins/internal/reporter"
)

func newGHClient(serverURL string) *github.Client {
	u, _ := url.Parse(serverURL + "/")
	client := github.NewClient(nil)
	client.BaseURL = u
	client.UploadURL = u
	return client
}

func TestReport_Success(t *testing.T) {
	var received github.RepoStatus

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := newGHClient(srv.URL)
	log := zerolog.New(io.Discard)
	r := reporter.New(client, log)

	err := r.Report(context.Background(), reporter.ReportRequest{
		Owner:        "owner",
		Repo:         "repo",
		SHA:          "abc123",
		WorkflowName: "ci",
		Status:       reporter.StatusSuccess,
		Description:  "all good",
		TargetURL:    "http://logs.example.com/1",
	})

	require.NoError(t, err)
	assert.Equal(t, "success", received.GetState())
	assert.Equal(t, "ghenkins/ci", received.GetContext())
	assert.Equal(t, "all good", received.GetDescription())
	assert.Equal(t, "http://logs.example.com/1", received.GetTargetURL())
}

func TestReport_DescriptionTruncation(t *testing.T) {
	var received github.RepoStatus

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := newGHClient(srv.URL)
	log := zerolog.New(io.Discard)
	r := reporter.New(client, log)

	longDesc := strings.Repeat("x", 200)
	err := r.Report(context.Background(), reporter.ReportRequest{
		Owner:        "owner",
		Repo:         "repo",
		SHA:          "abc123",
		WorkflowName: "ci",
		Status:       reporter.StatusPending,
		Description:  longDesc,
	})

	require.NoError(t, err)
	assert.Len(t, received.GetDescription(), 140)
}

func TestReport_RetryOn500(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := newGHClient(srv.URL)
	log := zerolog.New(io.Discard)
	r := reporter.New(client, log)

	err := r.Report(context.Background(), reporter.ReportRequest{
		Owner:        "owner",
		Repo:         "repo",
		SHA:          "abc123",
		WorkflowName: "ci",
		Status:       reporter.StatusFailure,
		Description:  "failed",
	})

	require.NoError(t, err)
	assert.Equal(t, int32(2), callCount.Load())
}

func TestReport_NoRetryOn404(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}))
	defer srv.Close()

	client := newGHClient(srv.URL)
	log := zerolog.New(io.Discard)
	r := reporter.New(client, log)

	err := r.Report(context.Background(), reporter.ReportRequest{
		Owner:        "owner",
		Repo:         "repo",
		SHA:          "abc123",
		WorkflowName: "ci",
		Status:       reporter.StatusError,
		Description:  "error",
	})

	require.Error(t, err)
	assert.Equal(t, int32(1), callCount.Load())
}
