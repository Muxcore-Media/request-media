package internal

import (
	"testing"

	metadatav1 "github.com/Muxcore-Media/metadata-tmdb/proto/metadatav1"
)

func TestMapSearchResultsPrefersExactTitleTVSeries(t *testing.T) {
	got := mapSearchResults("When Calls the Heart", metadatav1.MediaType_MEDIA_TYPE_UNSPECIFIED, []*metadatav1.SearchResult{
		{
			Id:          229749,
			Title:       "When Calls the Heart",
			ReleaseDate: "2013-10-05",
			MediaType:   metadatav1.MediaType_MEDIA_TYPE_MOVIE,
		},
		{
			Id:          937100,
			Title:       "When Calls the Heart: Christmas",
			ReleaseDate: "2016-12-25",
			MediaType:   metadatav1.MediaType_MEDIA_TYPE_MOVIE,
		},
		{
			Id:           61635,
			Name:         "When Calls the Heart",
			FirstAirDate: "2014-01-11",
			MediaType:    metadatav1.MediaType_MEDIA_TYPE_TV,
		},
		{
			Id:        1,
			Name:      "Someone Else",
			MediaType: metadatav1.MediaType_MEDIA_TYPE_TV,
		},
		{
			Id:        2,
			Name:      "A Person",
			MediaType: metadatav1.MediaType_MEDIA_TYPE_UNSPECIFIED,
		},
	})
	if len(got) != 4 {
		t.Fatalf("len=%d want 4 (skip person/unspecified)", len(got))
	}
	if got[0].ID != 61635 || got[0].MediaType != "tv" || got[0].Title != "When Calls the Heart" || got[0].Year != 2014 {
		t.Fatalf("first result = %+v, want TV series 61635", got[0])
	}
}

func TestMapSearchResultsMovieFilterDropsTV(t *testing.T) {
	got := mapSearchResults("When Calls the Heart", metadatav1.MediaType_MEDIA_TYPE_MOVIE, []*metadatav1.SearchResult{
		{Id: 61635, Name: "When Calls the Heart", MediaType: metadatav1.MediaType_MEDIA_TYPE_TV},
		{Id: 229749, Title: "When Calls the Heart", MediaType: metadatav1.MediaType_MEDIA_TYPE_MOVIE},
	})
	if len(got) != 1 || got[0].ID != 229749 || got[0].MediaType != "movie" {
		t.Fatalf("got %+v", got)
	}
}

func TestIsTVRequest(t *testing.T) {
	if !isTVRequest("tv") || !isTVRequest("Series") || isTVRequest("movie") || isTVRequest("") {
		t.Fatal("isTVRequest mismatch")
	}
}
