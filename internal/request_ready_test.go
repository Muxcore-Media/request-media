package internal

import (
	"context"
	"encoding/json"
	"testing"

	eventsv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/events/v1"
)

func TestRequestMatchesFileReady(t *testing.T) {
	movie := &requestRecord{ID: "r1", ItemType: "movie", ItemID: "movie-1", TMDBID: 27205, Status: StatusAdded, Title: "Inception"}
	if !requestMatchesFileReady(movie, fileReadyHint{MovieID: "movie-1", ItemType: "movie"}) {
		t.Fatal("expected movie_id match")
	}
	if !requestMatchesFileReady(movie, fileReadyHint{TMDBID: 27205, ItemType: "movie"}) {
		t.Fatal("expected tmdb match")
	}
	if requestMatchesFileReady(movie, fileReadyHint{TMDBID: 27205, ItemType: "tv"}) {
		t.Fatal("expected media type miss")
	}
	pending := &requestRecord{ID: "r2", ItemType: "movie", ItemID: "movie-1", Status: StatusPending}
	if requestMatchesFileReady(pending, fileReadyHint{MovieID: "movie-1"}) {
		t.Fatal("pending must not emit ready")
	}
	denied := &requestRecord{ID: "r3", ItemType: "movie", TMDBID: 27205, Status: StatusDenied}
	if requestMatchesFileReady(denied, fileReadyHint{TMDBID: 27205, ItemType: "movie"}) {
		t.Fatal("denied must not emit ready")
	}
	watchlisted := &requestRecord{ID: "r4", ItemType: "movie", ItemID: "movie-1", Status: StatusWatchlisted}
	if requestMatchesFileReady(watchlisted, fileReadyHint{MovieID: "movie-1"}) {
		t.Fatal("watchlisted must not emit ready")
	}
	requested := &requestRecord{ID: "r5", ItemType: "movie", TMDBID: 27205, Status: StatusRequested}
	if !requestMatchesFileReady(requested, fileReadyHint{TMDBID: 27205, ItemType: "movie"}) {
		t.Fatal("requested + tmdb should emit ready once playable")
	}
	workflow := &requestRecord{ID: "r6", ItemType: "tv", ItemID: "series-1", Status: StatusWorkflow}
	if !requestMatchesFileReady(workflow, fileReadyHint{SeriesID: "series-1", ItemType: "tv"}) {
		t.Fatal("workflow + series_id should emit ready")
	}
}

func TestParseFileReadyHint(t *testing.T) {
	hint, ok := parseFileReadyHint("media.movie.file.added", []byte(`{"movie_id":"movie-1"}`))
	if !ok || hint.MovieID != "movie-1" || hint.ItemType != "movie" {
		t.Fatalf("movie file: %+v ok=%v", hint, ok)
	}
	hint, ok = parseFileReadyHint("media.tv.episode.file.added", []byte(`{"series_id":"series-1","episode_id":"e1"}`))
	if !ok || hint.SeriesID != "series-1" || hint.ItemType != "tv" {
		t.Fatalf("tv file: %+v ok=%v", hint, ok)
	}
	hint, ok = parseFileReadyHint("media.file.imported", []byte(`{"tmdb_id":27205,"media_type":"movie","title":"Inception"}`))
	if !ok || hint.TMDBID != 27205 || hint.ItemType != "movie" {
		t.Fatalf("imported: %+v ok=%v", hint, ok)
	}
}

func TestPublishRequestReadyOnceDedupe(t *testing.T) {
	m := testModule(t)
	rec := &requestRecord{
		ID: "req_mv_ready", ItemType: "movie", ItemID: "movie-1",
		TMDBID: 27205, Title: "Inception", Year: 2010, Status: StatusAdded,
		RequestedBy: "alice",
	}
	m.saveRequest(rec)

	payload, _ := json.Marshal(map[string]any{"movie_id": "movie-1"})
	evt := &eventsv1.Event{Payload: payload}
	m.handleFileReadyEvent(context.Background(), "media.movie.file.added", evt)
	m.handleFileReadyEvent(context.Background(), "media.movie.file.added", evt)

	claimed, err := m.store.ClaimReadyNotified("req_mv_ready")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("expected request_id already claimed after first file-added")
	}
}
