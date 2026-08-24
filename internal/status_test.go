package internal

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
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

func TestDeriveAcquisitionStatus_StalledPassesThrough(t *testing.T) {
	got := deriveAcquisitionStatus("tv", 39297, "Last Man Standing",
		[]*automationv1.QueueItem{{ItemType: "tv", ItemId: "s00", TmdbId: 39297, SeasonNumber: 0, EpisodeNumber: 1, Missing: true}},
		[]*automationv1.DownloadRecord{{WantedItemId: "s00", Status: "stalled"}},
	)
	if got != "stalled" {
		t.Fatalf("got %q, want stalled", got)
	}
}

func TestDeriveAcquisitionStatus_ImportFailedPassesThrough(t *testing.T) {
	got := deriveAcquisitionStatus("movie", 218, "The Terminator",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		[]*automationv1.DownloadRecord{{WantedItemId: "m1", Status: "import_failed"}},
	)
	if got != "import_failed" {
		t.Fatalf("got %q, want import_failed", got)
	}
}

func TestDeriveAcquisitionStatus_FailedPassesThrough(t *testing.T) {
	got := deriveAcquisitionStatus("movie", 218, "The Terminator",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		[]*automationv1.DownloadRecord{{WantedItemId: "m1", Status: "failed"}},
	)
	if got != "failed" {
		t.Fatalf("got %q, want failed", got)
	}
}

func TestDeriveAcquisitionStatus_ImportFailedWinsOverSent(t *testing.T) {
	got := deriveAcquisitionStatus("tv", 100, "Show",
		[]*automationv1.QueueItem{
			{ItemType: "tv", ItemId: "s1e1", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 1, Missing: true},
			{ItemType: "tv", ItemId: "s1e2", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 2, Missing: true},
		},
		[]*automationv1.DownloadRecord{
			{WantedItemId: "s1e1", Status: "sent"},
			{WantedItemId: "s1e2", Status: "import_failed"},
		},
	)
	if got != "import_failed" {
		t.Fatalf("got %q, want import_failed", got)
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

func TestDeriveHistoryStatusFields_FromAutomationHistory(t *testing.T) {
	detail, label := deriveHistoryStatusFields("movie", 218, "The Terminator",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		[]*automationv1.DownloadRecord{{
			WantedItemId: "m1", Status: "import_failed",
			StatusDetail: "path not under a scanner watch directory",
			StatusLabel:  "Import failed: path not under a scanner watch directory",
		}},
	)
	if detail != "path not under a scanner watch directory" {
		t.Fatalf("detail = %q", detail)
	}
	if label != "Import failed: path not under a scanner watch directory" {
		t.Fatalf("label = %q", label)
	}
}

func TestDeriveHistoryStatusFields_OldBinaryEmptyFields(t *testing.T) {
	detail, label := deriveHistoryStatusFields("movie", 218, "The Terminator",
		[]*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		[]*automationv1.DownloadRecord{{WantedItemId: "m1", Status: "stalled"}},
	)
	if detail != "" || label != "" {
		t.Fatalf("want empty fallback, got detail=%q label=%q", detail, label)
	}
}

func TestDeriveHistoryStatusFields_PrefersHigherPriorityHistory(t *testing.T) {
	detail, label := deriveHistoryStatusFields("tv", 100, "Show",
		[]*automationv1.QueueItem{
			{ItemType: "tv", ItemId: "s1e1", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 1, Missing: true},
			{ItemType: "tv", ItemId: "s1e2", TmdbId: 100, SeasonNumber: 1, EpisodeNumber: 2, Missing: true},
		},
		[]*automationv1.DownloadRecord{
			{WantedItemId: "s1e1", Status: "sent", StatusLabel: "Downloading"},
			{WantedItemId: "s1e2", Status: "import_failed", StatusDetail: "scanner unavailable", StatusLabel: "Import failed: scanner unavailable"},
		},
	)
	if detail != "scanner unavailable" {
		t.Fatalf("detail = %q", detail)
	}
	if label != "Import failed: scanner unavailable" {
		t.Fatalf("label = %q", label)
	}
}

func TestListRequests_IncludesHistoryStatusFields(t *testing.T) {
	m := testModule(t)
	rec := &requestRecord{
		ID: "r1", ItemType: "movie", TMDBID: 218, Title: "The Terminator",
		Status: "import_failed", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	m.mu.Lock()
	m.requests[rec.ID] = rec
	m.mu.Unlock()

	stub := &stubAutomation{
		queue: []*automationv1.QueueItem{{ItemType: "movie", ItemId: "m1", TmdbId: 218, Missing: true}},
		hist: []*automationv1.DownloadRecord{{
			WantedItemId: "m1", Status: "import_failed",
			StatusDetail: "download path missing on disk",
			StatusLabel:  "Import failed: download path missing on disk",
		}},
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

	list, err := m.ListRequests(context.Background(), &requestmedia.ListRequestsRequest{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(list.GetRequests()) != 1 {
		t.Fatalf("len = %d", len(list.GetRequests()))
	}
	info := list.GetRequests()[0]
	if info.GetStatusDetail() != "download path missing on disk" {
		t.Fatalf("status_detail = %q", info.GetStatusDetail())
	}
	if info.GetStatusLabel() != "Import failed: download path missing on disk" {
		t.Fatalf("status_label = %q", info.GetStatusLabel())
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
}

func TestRefreshAcquisitionStatus_PersistsImportFailed(t *testing.T) {
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
		hist:  []*automationv1.DownloadRecord{{WantedItemId: "m1", Status: "import_failed"}},
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
	if got != "import_failed" {
		t.Fatalf("status = %q, want import_failed", got)
	}

	m2 := NewModule(Config{
		ID: "request-media-test", GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", DataDir: m.dataDir,
	})
	if err := m2.Init(context.Background()); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	defer func() { _ = m2.Stop(context.Background()) }()
	if m2.requests["r1"].Status != "import_failed" {
		t.Fatalf("persisted status = %q", m2.requests["r1"].Status)
	}
}
