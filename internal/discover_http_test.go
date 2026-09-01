package internal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/Muxcore-Media/contracts-metadata/muxcore/metadata/v1"
)

type browseFixtureMetadata struct {
	fixtureMetadata
}

func (browseFixtureMetadata) ListTrending(_ context.Context, req *metadatav1.ListTrendingRequest) (*metadatav1.ListTrendingResponse, error) {
	if req.GetMediaType() == metadatav1.TrendingMediaType_TRENDING_MEDIA_TYPE_TV {
		return &metadatav1.ListTrendingResponse{
			Results: []*metadatav1.SearchResult{{
				Id: 1396, Name: "Breaking Bad", Overview: "fixture tv",
				PosterPath: "/ggFHVNu6YYI5L9W6QN5CvfWgmd.jpg", FirstAirDate: "2008-01-20",
				VoteAverage: 8.9, MediaType: metadatav1.MediaType_MEDIA_TYPE_TV,
			}},
		}, nil
	}
	return &metadatav1.ListTrendingResponse{
		Results: []*metadatav1.SearchResult{{
			Id: 550, Title: "Fight Club", Overview: "fixture movie",
			PosterPath: "/pB8BM7pdSp6B6Ih7QZ4DrQ3PmJK.jpg", ReleaseDate: "1999-10-15",
			VoteAverage: 8.4, MediaType: metadatav1.MediaType_MEDIA_TYPE_MOVIE,
		}},
	}, nil
}

func (browseFixtureMetadata) ListPopular(_ context.Context, req *metadatav1.ListPopularRequest) (*metadatav1.ListPopularResponse, error) {
	if req.GetType() == metadatav1.MediaType_MEDIA_TYPE_TV {
		return &metadatav1.ListPopularResponse{
			Results: []*metadatav1.SearchResult{{
				Id: 1396, Name: "Breaking Bad", FirstAirDate: "2008-01-20",
				MediaType: metadatav1.MediaType_MEDIA_TYPE_TV,
			}},
		}, nil
	}
	return &metadatav1.ListPopularResponse{
		Results: []*metadatav1.SearchResult{{
			Id: 550, Title: "Fight Club", ReleaseDate: "1999-10-15",
			MediaType: metadatav1.MediaType_MEDIA_TYPE_MOVIE,
		}},
	}, nil
}

func (browseFixtureMetadata) GetMovieDetails(_ context.Context, req *metadatav1.GetMovieDetailsRequest) (*metadatav1.GetMovieDetailsResponse, error) {
	if req.GetId() != 550 {
		return nil, status.Errorf(codes.NotFound, "fixture movie %d", req.GetId())
	}
	return &metadatav1.GetMovieDetailsResponse{
		Id: 550, Title: "Fight Club", ReleaseDate: "1999-10-15", Overview: "fixture overview",
		PosterPath: "/pB8BM7pdSp6B6Ih7QZ4DrQ3PmJK.jpg", VoteAverage: 8.4,
	}, nil
}

func (browseFixtureMetadata) GetTVDetails(_ context.Context, req *metadatav1.GetTVDetailsRequest) (*metadatav1.GetTVDetailsResponse, error) {
	if req.GetId() != 1396 {
		return nil, status.Errorf(codes.NotFound, "fixture tv %d", req.GetId())
	}
	return &metadatav1.GetTVDetailsResponse{
		Id: 1396, Name: "Breaking Bad", FirstAirDate: "2008-01-20", Overview: "fixture tv overview",
		PosterPath: "/ggFHVNu6YYI5L9W6QN5CvfWgmd.jpg", VoteAverage: 8.9,
	}, nil
}

func wireBrowseFixture(t *testing.T, m *Module) string {
	t.Helper()
	addr := startGRPC(t, func(s *grpc.Server) {
		metadatav1.RegisterMetadataServiceServer(s, browseFixtureMetadata{})
	})
	m.findAddr = func(_ context.Context, capability string) (string, error) {
		if capability == "metadata" {
			return addr, nil
		}
		return "", status.Errorf(codes.NotFound, "no %s", capability)
	}
	return addr
}

func TestHTTPDiscover_MovieDetail(t *testing.T) {
	m, base, client := testModuleHTTP(t)
	wireBrowseFixture(t, m)

	resp, err := client.Get(base + "/api/discover/movie/550")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var detail discoverDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != 550 || detail.Title != "Fight Club" || detail.MediaType != "movie" {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestHTTPDiscover_TVTrendingAndPopular(t *testing.T) {
	m, base, client := testModuleHTTP(t)
	wireBrowseFixture(t, m)

	for _, path := range []string{
		"/api/discover/trending/tv",
		"/api/discover/popular/tv",
	} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var out struct {
			Results []searchResult `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if len(out.Results) != 1 || out.Results[0].ID != 1396 {
			t.Fatalf("%s results=%+v", path, out.Results)
		}
	}
}
