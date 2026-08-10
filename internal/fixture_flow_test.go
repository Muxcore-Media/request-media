package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mgmntv1 "github.com/Muxcore-Media/media-movies/proto/mgmntv1"
	tvmgmtv1 "github.com/Muxcore-Media/media-tvshows/proto/tvmgmtv1"
	metadatav1 "github.com/Muxcore-Media/metadata-tmdb/proto/metadatav1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

// fixtureMetadata mirrors metadata-tmdb TMDB_FIXTURE corpus (Fight Club / Breaking Bad).
// Offline only — no network; used so request-media CI can exercise real search→request
// flows without dialing live TMDB or requiring a sibling checkout.
type fixtureMetadata struct {
	metadatav1.UnimplementedMetadataServiceServer
}

func (fixtureMetadata) Search(_ context.Context, req *metadatav1.SearchRequest) (*metadatav1.SearchResponse, error) {
	q := strings.ToLower(strings.TrimSpace(req.GetQuery()))
	switch req.GetType() {
	case metadatav1.MediaType_MEDIA_TYPE_TV:
		if strings.Contains(q, "breaking") || strings.Contains(q, "bad") {
			return &metadatav1.SearchResponse{
				Results: []*metadatav1.SearchResult{{
					Id: 1396, Name: "Breaking Bad", Title: "",
					Overview: "A high school chemistry teacher diagnosed with inoperable lung cancer turns to manufacturing and selling methamphetamine in order to secure his family's future.",
					PosterPath: "/ggFHVNu6YYI5L9W6QN5CvfWgmd.jpg", FirstAirDate: "2008-01-20",
					VoteAverage: 8.918, MediaType: metadatav1.MediaType_MEDIA_TYPE_TV,
				}},
				TotalResults: 1, TotalPages: 1, Page: 1,
			}, nil
		}
	default: // movie
		if strings.Contains(q, "fight") || strings.Contains(q, "club") {
			return &metadatav1.SearchResponse{
				Results: []*metadatav1.SearchResult{{
					Id: 550, Title: "Fight Club",
					Overview: "An insomniac office worker and a devil-may-care soap maker form an underground fight club that evolves into something much more.",
					PosterPath: "/pB8BM7pdSp6B6Ih7QZ4DrQ3PmJK.jpg", ReleaseDate: "1999-10-15",
					VoteAverage: 8.433, MediaType: metadatav1.MediaType_MEDIA_TYPE_MOVIE,
				}},
				TotalResults: 1, TotalPages: 1, Page: 1,
			}, nil
		}
	}
	return &metadatav1.SearchResponse{Results: nil, Page: 1}, nil
}

func (fixtureMetadata) GetMovieDetails(_ context.Context, req *metadatav1.GetMovieDetailsRequest) (*metadatav1.GetMovieDetailsResponse, error) {
	if req.GetTmdbId() != 550 {
		return nil, status.Errorf(codes.NotFound, "fixture movie %d", req.GetTmdbId())
	}
	return &metadatav1.GetMovieDetailsResponse{
		Id: 550, Title: "Fight Club", ReleaseDate: "1999-10-15", Runtime: 139, ImdbId: "tt0137523",
	}, nil
}

func (fixtureMetadata) GetTVDetails(_ context.Context, req *metadatav1.GetTVDetailsRequest) (*metadatav1.GetTVDetailsResponse, error) {
	if req.GetTmdbId() != 1396 {
		return nil, status.Errorf(codes.NotFound, "fixture tv %d", req.GetTmdbId())
	}
	return &metadatav1.GetTVDetailsResponse{
		Id: 1396, Name: "Breaking Bad", FirstAirDate: "2008-01-20", NumberOfSeasons: 5, NumberOfEpisodes: 62,
	}, nil
}

func testModuleHTTP(t *testing.T) (*Module, string) {
	t.Helper()
	m := testModule(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	base := "http://" + m.httpLis.Addr().String()
	return m, base
}

func wireFixtureStack(t *testing.T, m *Module) (movies *stubMovies, tv *stubTV) {
	t.Helper()
	metaAddr := startGRPC(t, func(s *grpc.Server) {
		metadatav1.RegisterMetadataServiceServer(s, fixtureMetadata{})
	})
	movies = &stubMovies{}
	movieAddr := startGRPC(t, func(s *grpc.Server) {
		mgmntv1.RegisterMovieManagementServiceServer(s, movies)
	})
	tv = &stubTV{}
	tvAddr := startGRPC(t, func(s *grpc.Server) {
		tvmgmtv1.RegisterTvManagementServiceServer(s, tv)
	})
	m.findAddr = func(_ context.Context, capability string) (string, error) {
		switch capability {
		case "metadata":
			return metaAddr, nil
		case "media.library.movie", "media.library.movies", "media.library":
			return movieAddr, nil
		case "media.library.tv":
			return tvAddr, nil
		case "workflow.engine":
			return "", fmt.Errorf("no workflow")
		default:
			return "", fmt.Errorf("no %s", capability)
		}
	}
	return movies, tv
}

func TestAdminHTTP_FixtureMetadata_MovieAndTVFlows(t *testing.T) {
	m, base := testModuleHTTP(t)
	movies, tv := wireFixtureStack(t, m)

	// Admin entry: index HTML
	idxResp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer idxResp.Body.Close()
	idxBody, _ := io.ReadAll(idxResp.Body)
	if idxResp.StatusCode != http.StatusOK || !bytes.Contains(idxBody, []byte("Request Media")) {
		t.Fatalf("admin index: status=%d body=%q", idxResp.StatusCode, truncateBytes(idxBody, 200))
	}
	if !bytes.Contains(idxBody, []byte("/api/search")) || !bytes.Contains(idxBody, []byte("mediaType")) {
		t.Fatal("admin index missing search/type controls")
	}

	// Movie search → request (Fight Club, TMDB_FIXTURE id 550)
	searchMovie, err := http.Get(base + "/api/search?q=Fight+Club&type=movie")
	if err != nil {
		t.Fatalf("search movie: %v", err)
	}
	defer searchMovie.Body.Close()
	var movieSearch struct {
		Results []searchResult `json:"results"`
		Error   string         `json:"error"`
	}
	if err := json.NewDecoder(searchMovie.Body).Decode(&movieSearch); err != nil {
		t.Fatalf("decode movie search: %v", err)
	}
	if movieSearch.Error != "" || len(movieSearch.Results) != 1 || movieSearch.Results[0].ID != 550 {
		t.Fatalf("movie fixture search: %+v", movieSearch)
	}
	if movieSearch.Results[0].Title != "Fight Club" || movieSearch.Results[0].Year != 1999 {
		t.Fatalf("movie result: %+v", movieSearch.Results[0])
	}

	reqMovieBody := `{"tmdbId":550,"title":"Fight Club","year":1999,"overview":"soap","poster":"/p.jpg","type":"movie"}`
	reqMovie, err := http.Post(base+"/api/request", "application/json", strings.NewReader(reqMovieBody))
	if err != nil {
		t.Fatalf("POST movie: %v", err)
	}
	defer reqMovie.Body.Close()
	var movieOut map[string]string
	if err := json.NewDecoder(reqMovie.Body).Decode(&movieOut); err != nil {
		t.Fatalf("decode movie request: %v", err)
	}
	if movieOut["status"] != "added" || movieOut["movieId"] != "movie-1" || movieOut["type"] != "movie" {
		t.Fatalf("movie request out: %+v", movieOut)
	}
	if movies.last == nil || movies.last.GetTmdbId() != 550 {
		t.Fatalf("AddMovie not called with fixture id: %+v", movies.last)
	}

	// TV search → request (Breaking Bad, TMDB_FIXTURE id 1396)
	searchTV, err := http.Get(base + "/api/search?q=Breaking+Bad&type=tv")
	if err != nil {
		t.Fatalf("search tv: %v", err)
	}
	defer searchTV.Body.Close()
	var tvSearch struct {
		Results []searchResult `json:"results"`
		Error   string         `json:"error"`
	}
	if err := json.NewDecoder(searchTV.Body).Decode(&tvSearch); err != nil {
		t.Fatalf("decode tv search: %v", err)
	}
	if tvSearch.Error != "" || len(tvSearch.Results) != 1 || tvSearch.Results[0].ID != 1396 {
		t.Fatalf("tv fixture search: %+v", tvSearch)
	}
	if tvSearch.Results[0].Title != "Breaking Bad" || tvSearch.Results[0].Type != "tv" {
		t.Fatalf("tv result: %+v", tvSearch.Results[0])
	}

	reqTVBody := `{"tmdbId":1396,"title":"Breaking Bad","year":2008,"overview":"chem","type":"tv"}`
	reqTV, err := http.Post(base+"/api/request", "application/json", strings.NewReader(reqTVBody))
	if err != nil {
		t.Fatalf("POST tv: %v", err)
	}
	defer reqTV.Body.Close()
	var tvOut map[string]string
	if err := json.NewDecoder(reqTV.Body).Decode(&tvOut); err != nil {
		t.Fatalf("decode tv request: %v", err)
	}
	if tvOut["status"] != "added" || tvOut["seriesId"] != "series-1" || tvOut["type"] != "tv" {
		t.Fatalf("tv request out: %+v", tvOut)
	}
	if tv.last == nil || tv.last.GetTmdbId() != 1396 {
		t.Fatalf("AddTVShow not called with fixture id: %+v", tv.last)
	}

	// History list
	listResp, err := http.Get(base + "/api/requests")
	if err != nil {
		t.Fatalf("GET /api/requests: %v", err)
	}
	defer listResp.Body.Close()
	var list []requestRecord
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode requests: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("expected movie+tv history, got %d", len(list))
	}
	seenMovie, seenTV := false, false
	for _, rec := range list {
		if rec.TMDBID == 550 && rec.ItemType == "movie" {
			seenMovie = true
		}
		if rec.TMDBID == 1396 && rec.ItemType == "tv" {
			seenTV = true
		}
	}
	if !seenMovie || !seenTV {
		t.Fatalf("history missing fixture items: %+v", list)
	}
}

func TestGRPC_FixtureMetadata_RequestMovieAndTV(t *testing.T) {
	m := testModule(t)
	wireFixtureStack(t, m)

	// Resolve via HTTP search path semantics through metadata client + gRPC request.
	meta, conn, err := m.metadataClient(context.Background())
	if err != nil {
		t.Fatalf("metadataClient: %v", err)
	}
	defer conn.Close()

	mv, err := meta.Search(context.Background(), &metadatav1.SearchRequest{
		Query: "Fight Club", Type: metadatav1.MediaType_MEDIA_TYPE_MOVIE, Page: 1,
	})
	if err != nil || len(mv.GetResults()) != 1 || mv.GetResults()[0].GetId() != 550 {
		t.Fatalf("fixture movie search: %+v err=%v", mv, err)
	}
	movieResp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: mv.GetResults()[0].GetId(), Title: mv.GetResults()[0].GetTitle(), Year: 1999,
	})
	if err != nil || movieResp.GetStatus() != "added" {
		t.Fatalf("RequestMovie: %+v err=%v", movieResp, err)
	}

	tvHit, err := meta.Search(context.Background(), &metadatav1.SearchRequest{
		Query: "Breaking Bad", Type: metadatav1.MediaType_MEDIA_TYPE_TV, Page: 1,
	})
	if err != nil || len(tvHit.GetResults()) != 1 || tvHit.GetResults()[0].GetId() != 1396 {
		t.Fatalf("fixture tv search: %+v err=%v", tvHit, err)
	}
	title := tvHit.GetResults()[0].GetName()
	tvResp, err := m.RequestTV(context.Background(), &requestmedia.RequestTVRequest{
		TmdbId: tvHit.GetResults()[0].GetId(), Title: title, Year: 2008, SeasonNumber: 1,
	})
	if err != nil || tvResp.GetStatus() != "added" {
		t.Fatalf("RequestTV: %+v err=%v", tvResp, err)
	}
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
