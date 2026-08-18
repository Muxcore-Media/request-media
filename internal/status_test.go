package internal

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	automationv1 "github.com/Muxcore-Media/media-automation/proto/automationv1"
)

func TestDeriveAcquisitionStatus_MovieDownloading(t *testing.T) {
	got := deriveAcquisitionStatus("movie", 218, "The Terminator",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		[]*automationv1.DownloadRecord{{WantedItemId: "m1", Status: "sent"}},
	)
	if got != "downloading" {
		t.Fatalf("got %q, want downloading", got)
	}
}

func TestDeriveAcquisitionStatus_StalledIsDownloading(t *testing.T) {
	got := deriveAcquisitionStatus("tv", 39297, "Last Man Standing",
		[]*automationv1.QueueItem{{ItemType: "tv", ItemId: "s00", TmdbId: 39297, SeasonNumber: 0, EpisodeNumber: 1, Missing: true}},
		[]*automationv1.DownloadRecord{{WantedItemId: "s00", Status: "stalled"}},
	)
	if got != "downloading" {
		t.Fatalf("got %q, want downloading", got)
	}
}

func TestDeriveAcquisitionStatus_MovieAvailable(t *testing.T) {
	got := deriveAcquisitionStatus("movie", 218, "The Terminator",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: false}},
		nil,
	)
	if got != "available" {
		t.Fatalf("got %q, want available", got)
	}
}

func TestDeriveAcquisitionStatus_TVIgnoresSeasonZeroPlaceholders(t *testing.T) {
	got := deriveAcquisitionStatus("tv", 100, "Show",
		[]*automationv1.QueueItem{
			{ItemType: "tv", ItemId: "s00", TmdbId: 100, SeasonNumber: 0, EpisodeNumber: 12, Missing: true},
			{ItemType: "tv", ItemId: "s1e1", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 1, Missing: false},
		},
		nil,
	)
	if got != "available" {
		t.Fatalf("got %q, want available (S00 dummy must not block)", got)
	}
}

func TestDeriveAcquisitionStatus_TVPartialDownloading(t *testing.T) {
	got := deriveAcquisitionStatus("tv", 100, "Show",
		[]*automationv1.QueueItem{
			{ItemType: "tv", ItemId: "s1e1", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 1, Missing: false},
			{ItemType: "tv", ItemId: "s1e2", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 2, Missing: true},
		},
		[]*automationv1.DownloadRecord{{WantedItemId: "s1e2", Status: "completed"}},
	)
	if got != "downloading" {
		t.Fatalf("got %q, want downloading", got)
	}
}

func TestDeriveAcquisitionStatus_MissingWantedIsSearching(t *testing.T) {
	got := deriveAcquisitionStatus("tv", 100, "Little Bear",
		[]*automationv1.QueueItem{{ItemType: "tv", ItemId: "pack", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 0, Missing: true}},
		nil,
	)
	if got != "searching" {
		t.Fatalf("got %q, want searching", got)
	}
}

func TestDeriveAcquisitionStatus_MovieMissingIsSearching(t *testing.T) {
	got := deriveAcquisitionStatus("movie", 218, "The Terminator",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		nil,
	)
	if got != "searching" {
		t.Fatalf("got %q, want searching", got)
	}
}

func TestDeriveAcquisitionStatus_NoQueueMatch(t *testing.T) {
	got := deriveAcquisitionStatus("movie", 1, "Nope",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 999, Missing: true}},
		nil,
	)
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestRefreshAcquisitionStatus_PersistsDownloading(t *testing.T) {
	m := testModule(t)
	rec := &requestRecord{
		ID: "r1", ItemType: "movie", TMDBID: 218, Title: "The Terminator",
		Status: "added", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	m.mu.Lock()
	m.requests[rec.ID] = rec
	m.mu.Unlock()
	if err := m.store.Put(toStoreRecord(rec)); err != nil {
		t.Fatalf("put: %v", err)
	}

	stub := &stubAutomation{
		queue: []*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		hist:  []*automationv1.DownloadRecord{{WantedItemId: "m1", Status: "sent"}},
	}
	addr := startGRPC(t, func(s *grpc.Server) {
		automationv1.RegisterAutomationServiceServer(s, stub)
	})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	m.mu.Lock()
	m.automationClient = automationv1.NewAutomationServiceClient(conn)
	m.mu.Unlock()

	m.refreshAcquisitionStatus(context.Background())
	m.mu.RLock()
	got := m.requests["r1"].Status
	m.mu.RUnlock()
	if got != "downloading" {
		t.Fatalf("status = %q, want downloading", got)
	}

	m2 := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", DataDir: m.dataDir,
	})
	if err := m2.Init(context.Background()); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	defer m2.Stop(context.Background())
	if m2.requests["r1"].Status != "downloading" {
		t.Fatalf("persisted status = %q", m2.requests["r1"].Status)
	}
}
